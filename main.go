package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/sync/semaphore"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// Define the scope for read-only metadata access
const driveMetadataScope = "https://www.googleapis.com/auth/drive.readonly"

// getClient uses a client ID and secret to retrieve a token
// from a web flow, then saves the token to a file.
func getClient(config *oauth2.Config) *http.Client {
	// The file token.json stores the user's access and refresh tokens.
	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokFile, tok)
	}
	return config.Client(context.Background(), tok)
}

// getTokenFromWeb retrieves a token from a web-based authorization flow.
func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser:\n%v\n", authURL)
	fmt.Print("Then type the authorization code: ")

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code: %v", err)
	}

	tok, err := config.Exchange(context.TODO(), strings.TrimSpace(authCode))
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}
	return tok
}

// tokenFromFile retrieves a token from a file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// saveToken saves a token to a file.
func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

// getFolderPath recursively fetches parent folders to build the full path.
func getFolderPath(srv *drive.Service, file *drive.File) (string, error) {
	if len(file.Parents) == 0 {
		return "", nil // File is in the root
	}

	var pathParts []string
	parentID := file.Parents[0] // Use the first parent

	for {
		parent, err := srv.Files.Get(parentID).Fields("name,parents").Do()
		if err != nil {
			return "", fmt.Errorf("unable to retrieve parent folder: %v", err)
		}
		// Prepend the folder name to our path parts
		pathParts = append([]string{parent.Name}, pathParts...)

		if len(parent.Parents) == 0 {
			break // Reached the root
		}
		parentID = parent.Parents[0]
	}

	return strings.Join(pathParts, "/"), nil
}

func main() {
	ctx := context.Background()

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Unable to get home directory: %v", err)
	}
	b, err := os.ReadFile(fmt.Sprintf("%s/.credentials.json", home))
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, driveMetadataScope)
	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}

	client := getClient(config)
	driveService, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Drive client: %v", err)
	}

	sigChan := make(chan os.Signal, 1)

	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Print("The application doesn't terminate with Ctrl+C, use Ctrl+D instead")
	}()

	scanner := bufio.NewScanner(os.Stdin)
	sem := semaphore.NewWeighted(int64(10))
	var wg sync.WaitGroup
	for scanner.Scan() {
		wg.Add(1)
		fileID := scanner.Text()
		go func(fileID string) {
			defer wg.Done()
			if fileID == "" {
				return
			}

			sem.Acquire(ctx, 1)
			defer sem.Release(1)

			file, err := driveService.Files.Get(fileID).Fields("name,parents").Do()
			if err != nil {
				log.Printf("Unable to retrieve file: %v", err)
				return
			}
			p, err := getFolderPath(driveService, file)
			if err != nil {
				log.Printf("Unable to retrieve folder path: %v", err)
				return
			}
			resp, err := driveService.Files.Get(fileID).Download()
			if err != nil {
				log.Printf("Unable to download file: %v", err)
				return
			}
			defer resp.Body.Close()

			if p == "" {
				p = "./"
			}
			if err = os.MkdirAll(p, 0755); err != nil {
				log.Printf("Unable to create destination folder: %s\n", p)
				return
			}
			outFile, err := os.Create(fmt.Sprintf("%s%s", p, file.Name))
			if err != nil {
				log.Printf("Unable to create download file")
				return
			}
			defer outFile.Close()
			_, err = io.Copy(outFile, resp.Body)
			if err != nil {
				log.Printf("Unable to write file content: %v", err)
				return
			}
		}(fileID)
	}
	wg.Wait()
}
