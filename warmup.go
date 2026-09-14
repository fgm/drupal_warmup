// This program concurrently loads a specified list of URLs, ignoring their content.
//
// To prevent thundering herd problems, it first performs authentication, then
// the first page fetch non-concurrently, enabling the possible first
// cache rebuild in the server to occur with minimal load.
//
// Its main purpose is to perform cache warming.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

var phpStormDebugCookie = http.Cookie{
	Name:     "XDEBUG_SESSION",
	Value:    "PHPSTORM",
	Path:     "/",
	Expires:  time.Now().Add(1 * time.Hour),
	MaxAge:   0,
	Secure:   false,
	HttpOnly: false,
}

func blockRedirect(req *http.Request, via []*http.Request) error {
	log.Printf("Checking redirect on %s via: ", req.URL.String())
	for _, req := range via {
		log.Printf(" - %s", req.URL.String())
	}
	return http.ErrUseLastResponse
}

func fetchURL(targetURL *url.URL, method, baUser, baPass string, body io.Reader, jar http.CookieJar, timeout time.Duration, wg *sync.WaitGroup, sem chan struct{}) ([]byte, error) {
	defer func() {
		if wg != nil {
			wg.Done()
		}
	}()

	// Acquire a semaphore to control concurrency
	if sem != nil {
		sem <- struct{}{}
		defer func() { <-sem }()
	}

	// log.Printf("Fetching %s\n", targetURL)
	t0 := time.Now()
	req, err := http.NewRequest(method, targetURL.String(), body)
	if err != nil {
		return nil, fmt.Errorf("error building request for %s: %w\n", targetURL, err)
	}
	if baUser != "" {
		req.SetBasicAuth(baUser, baPass)
	}
	client := http.Client{
		Jar:           jar,
		Transport:     http.DefaultTransport,
		CheckRedirect: nil,
		Timeout:       timeout,
	}
	if method == http.MethodPost {
		ct := req.Header.Get("Content-Type")
		if ct == "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error fetching %s: %v\n", targetURL, err)
	}

	fmt.Printf("Fetched %s %s, Status Code: %d, %v\n", method, targetURL, resp.StatusCode, time.Now().Sub(t0))
	if resp.Body == nil {
		return nil, fmt.Errorf("no response body for %q: %v/n", targetURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error ready body for %q: %v/n", targetURL, err)
	}
	return bs, nil
}

func tokenMatchesNodeNameAndName(tok html.Token, nodeName, pattern string) (value string, found bool) {
	rx := regexp.MustCompile(pattern)
	if strings.ToUpper(tok.Data) != strings.ToUpper(nodeName) {
		return "", false
	}
	for _, attr := range tok.Attr {
		if attr.Namespace != "" {
			continue
		}
		if attr.Key == "name" && rx.MatchString(attr.Val) {
			return rx.FindString(attr.Val), true
		}
	}
	return "", false
}

func tokenValue(tok html.Token) string {
	for _, attr := range tok.Attr {
		if attr.Key == "value" {
			return attr.Val
		}
	}
	// Return the default: empty string.
	return ""
}

// getLoginPageInputs returns the values in the user and password fields on the user login form.
func getLoginPageInputs(userURL *url.URL, baUser, baPass string, timeout time.Duration) (
	string, string, string, string, string, error,
) {
	body, err := fetchURL(userURL, http.MethodGet, baUser, baPass, nil, nil, timeout, nil, nil)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("failed logging in: %w", err)
	}

	tokenizer := html.NewTokenizer(strings.NewReader(string(body)))
	idFound, passFound, buildIDFound, formIDFound, opFound := false, false, false, false, false
	name := ""
	pass := ""
	buildID := ""
	formID := ""
	op := ""

	// Loop through the HTML tokens
tokenLoop:
	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			// End of the document
			break tokenLoop
		case html.StartTagToken, html.SelfClosingTagToken:
			// Get the token
			tok := tokenizer.Token()
			nameAttr, ok := tokenMatchesNodeNameAndName(tok, "input", "(name|pass|form_build_id|form_id|op)")
			if !ok {
				continue
			}
			value := tokenValue(tok)
			switch nameAttr {
			case "name":
				name = value
				idFound = true
			case "pass":
				pass = value
				passFound = true
			case "form_build_id":
				buildID = value
				buildIDFound = true
			case "form_id":
				formID = value
				formIDFound = true
			case "op":
				op = value
				opFound = true
			}
		default:
			// Ignore
		}
	}
	for _, crit := range []struct {
		check   bool
		message string
	}{
		{idFound, "a name field"},
		{passFound, "a pass field"},
		{buildIDFound, "a form_build_id"},
		{formIDFound, "a form_id"},
		{opFound, "an op button"},
	} {
		if !crit.check {
			return "", "", "", "", "", fmt.Errorf("login form does not contain %s", crit.message)
		}
	}
	return name, pass, buildID, formID, op, nil
}

// postLoginCredentials may mutate the jar contents.
func postLoginCredentials(userURL *url.URL, baUser, baPass string, jar http.CookieJar, timeout time.Duration, drUser, drPass, buildID, formID, op string) error {
	requestBody := strings.NewReader(url.Values{
		"name":          []string{drUser},
		"pass":          []string{drPass},
		"form_build_id": []string{buildID},
		"form_id":       []string{formID},
		"op":            []string{op},
	}.Encode())
	responseBody, err := fetchURL(userURL, http.MethodPost, baUser, baPass, requestBody, jar, timeout, nil, nil)
	if err != nil {
		return fmt.Errorf("failed logging in: %w", err)
	}

	_, _ = fmt.Fprintf(io.Discard, "%s\n", responseBody)
	return err
}

// drupalLogin will usually mutate the jar, e.g. adding a SESSxxxx cookie on success.
func drupalLogin(u *url.URL, baUser, baPass, drUser, drPass string, jar http.CookieJar, timeout time.Duration) error {
	var err error
	var userURL = *u
	userURL.Path, err = url.JoinPath(u.Path, "/user/login")
	if err != nil {
		return fmt.Errorf("failed building login path: %w", err)
	}
	name, pass, buildID, formID, op, err := getLoginPageInputs(&userURL, baUser, baPass, timeout)
	if name != "" || pass != "" {
		log.Printf("Existing data: user=%q pass=%q", name, pass)
	}
	if err != nil {
		return fmt.Errorf("failed getting valid login form: %w", err)
	}
	err = postLoginCredentials(&userURL, baUser, baPass, jar, timeout, drUser, drPass, buildID, formID, op)
	if err != nil {
		return err
	}
	cookies := jar.Cookies(u)
	// HTTPS uses a SSESS<id> cookie, HTTP uses a SESS<id>.
	sessRx := regexp.MustCompile("^S?SESS[0-9a-fA-F]{32}$")
	for _, cookie := range cookies {
		if sessRx.MatchString(cookie.Name) {
			return nil
		}
	}
	return fmt.Errorf("no session cookie found")
}

func getFlags() (string, string, time.Duration, time.Duration, time.Duration, int, string, string, string, string, int) {
	base := flag.String("base", "http://example.com", "The base URL at which the site resides")
	inFile := flag.String("list-file", "-", "The file from which to load the list of URLs to hit. Use '-' for stdin, which is the default.")
	loginTimeout := flag.Duration("login-timeout", 180*time.Second, "How long to wait for a login")
	firstTimeout := flag.Duration("first-timeout", 60*time.Second, "How long to wait for a first page after cache clear")
	defaultTimeout := flag.Duration("timeout", 10*time.Second, "How long to wait for a normal page")
	concurrency := flag.Int("c", 4, "How many requests to emit concurrently")
	baUser := flag.String("bauser", "", "The user name for basic auth. BasicAuth will not trigger if it is empty.")
	baPass := flag.String("bapass", "", "The password for basic auth")
	drUser := flag.String("druser", "admin", "The user name for Drupal login")
	drPass := flag.String("drpass", "", "The password for Drupal login")
	runs := flag.Int("runs", 1, "The number of runs")
	flag.Parse()
	return *base, *inFile, *loginTimeout, *firstTimeout, *defaultTimeout, *concurrency, *baUser, *baPass, *drUser, *drPass, *runs
}

func loadPaths(inFile string) ([]string, error) {
	var (
		err error
		in  io.Reader
	)
	switch inFile {
	case "-":
		in = os.Stdin
	default:
		in, err = os.Open(inFile)
		if err != nil {
			return nil, fmt.Errorf("could not open list-file %q: %v", inFile, err)
		}
	}
	scanner := bufio.NewScanner(in)
	urls := make([]string, 0, 10)
	for scanner.Scan() {
		urls = append(urls, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed scanning URL: %v", err)
	}
	return urls, nil
}

func main() {
	loadCount := 0
	base, inFile, loginTimeout, firstTimeout, defaultTimeout, concurrency, baUser, baPass, drUser, drPass, runs := getFlags()
	paths, err := loadPaths(inFile)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Loading %d URLs with concurrency %d", len(paths), concurrency)
	signaling := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	t0 := time.Now()
	defer func() {
		log.Printf("Loaded %d URLs in %v", loadCount, time.Now().Sub(t0))
	}()

	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatalf("building cookie jar: %w", err)
	}
	u, err := url.Parse(base)
	if err != nil {
		log.Fatalf("parsing base URL %q: %v", base, err)
	}
	jar.SetCookies(u, []*http.Cookie{})
	loadCount += 2 // login does GET + POST.
	if err := drupalLogin(u, baUser, baPass, drUser, drPass, jar, loginTimeout); err != nil {
		log.Printf("failed logging in: %v", err)
		return
	}
	log.Printf("Cookies for %q:", u.String())
	for _, c := range jar.Cookies(u) {
		log.Printf("- %s", c)
	}
	// Run first GET in isolation to avoid overloading server while rebuilding container.
	{
		path := paths[0]
		userURL := u
		userURL.Path = path
		log.Println("First fetch to avoid thundering herd on cache rebuild")
		_, _ = fetchURL(u, http.MethodGet, baUser, baPass, nil, jar, firstTimeout, nil, signaling)
		loadCount++
	}

	paths = paths[1:]
	u.RawQuery = ""
	for run := 0; run < runs; run++ {
		for _, path := range paths {
			wg.Add(1)
			// See https://github.com/golang/go/issues/38351 and https://github.com/golang/go/issues/41733
			u2 := *u
			u2.Path = path
			loadCount++
			go func() { _, _ = fetchURL(&u2, http.MethodGet, baUser, baPass, nil, jar, defaultTimeout, &wg, signaling) }()
		}

		wg.Wait()
	}
}
