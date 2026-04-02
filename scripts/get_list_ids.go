// get_list_ids.go — One-time script to authenticate with Google and print your Task list IDs.
//
// Usage:
//   go run scripts/get_list_ids.go
//
// This will:
//   1. Open a browser for OAuth2 consent
//   2. Save the token to token.json
//   3. Print all your Google Task list names and IDs

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	gtasks "google.golang.org/api/tasks/v1"
)

const (
	credFile  = "credentials.json"
	tokenFile = "token.json"
)

func main() {
	ctx := context.Background()

	b, err := os.ReadFile(credFile)
	if err != nil {
		log.Fatalf("Unable to read %s: %v\nMake sure you have downloaded your OAuth credentials from Google Cloud Console.", credFile, err)
	}

	config, err := google.ConfigFromJSON(b, gtasks.TasksScope)
	if err != nil {
		log.Fatalf("Unable to parse credentials: %v", err)
	}

	tok := getToken(config)
	saveToken(tokenFile, tok)

	client := config.Client(ctx, tok)
	svc, err := gtasks.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to create Tasks service: %v", err)
	}

	lists, err := svc.Tasklists.List().MaxResults(100).Do()
	if err != nil {
		log.Fatalf("Unable to list task lists: %v", err)
	}

	fmt.Println("\n📋 Your Google Task Lists:")
	fmt.Println("─────────────────────────────────────────")
	for _, list := range lists.Items {
		fmt.Printf("  %-20s → %s\n", list.Title, list.Id)
	}
	fmt.Println("─────────────────────────────────────────")
	fmt.Printf("\n✅ Token saved to %s\n", tokenFile)
	fmt.Println("Copy the list IDs above into your .env file.")
}

func getToken(config *oauth2.Config) *oauth2.Token {
	// Try to load existing token first — only reuse if still valid
	if tok, err := loadExistingToken(tokenFile); err == nil && tok.Valid() {
		fmt.Println("✅ Using existing valid token from", tokenFile)
		return tok
	}

	// Start local server to receive the OAuth callback
	codeCh := make(chan string, 1)
	config.RedirectURL = "http://localhost:9090/callback"

	srv := &http.Server{Addr: ":9090"}
	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		codeCh <- code
		fmt.Fprintf(w, "<h1>✅ Authorization successful!</h1><p>You can close this tab.</p>")
	})

	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("\n🔗 Open this URL in your browser:\n\n%s\n\n", authURL)

	code := <-codeCh
	_ = srv.Shutdown(context.Background())

	tok, err := config.Exchange(context.Background(), code)
	if err != nil {
		log.Fatalf("Unable to exchange auth code for token: %v", err)
	}
	return tok
}

func loadExistingToken(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	return tok, json.NewDecoder(f).Decode(tok)
}

func saveToken(path string, tok *oauth2.Token) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("Unable to save token to %s: %v", path, err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(tok); err != nil {
		log.Fatalf("Unable to encode token: %v", err)
	}
}
