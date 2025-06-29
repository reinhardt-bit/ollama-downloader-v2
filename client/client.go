package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type PullRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

type OllamaResponse struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
}

type ProgressMsg struct {
	Status    string
	Completed int64
	Total     int64
}

type TimeoutMsg struct{}

type ErrorMsg struct {
	Err error
}

func PullModel(ctx context.Context, model string, host string, progressCh chan<- tea.Msg, continueUntilComplete bool, userChoiceCh <-chan string) {
	go func() {
		// A single defer ensures the channel is always closed on exit.
		defer close(progressCh)

		pullReq := PullRequest{
			Model:  model,
			Stream: true,
		}

		body, err := json.Marshal(pullReq)
		if err != nil {
			log.Printf("Error marshalling request: %v", err)
			progressCh <- ErrorMsg{Err: fmt.Errorf("error marshalling request: %w", err)}
			return
		}

		// FIX: Use the default client so it can be configured in tests.
		client := http.DefaultClient
		var downloadFinished bool

	retryLoop:
		for {
			// Check for cancellation or user quit before starting a new attempt.
			select {
			case <-ctx.Done():
				log.Println("Main context cancelled (top of loop).")
				return
			case choice := <-userChoiceCh:
				if choice == "Quit" {
					log.Println("User chose to quit (top of loop).")
					return
				}
			default:
				// Continue
			}

			// This anonymous function scopes a single download attempt,
			// correctly managing its context and deferred calls.
			err := func() error {
				reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Second)
				defer reqCancel()

				req, err := http.NewRequestWithContext(reqCtx, "POST", host+"/api/pull", bytes.NewBuffer(body))
				if err != nil {
					return fmt.Errorf("error creating request: %w", err)
				}
				req.Header.Set("Content-Type", "application/json")

				resp, err := client.Do(req)
				if err != nil {
					return err // Return error to the outer loop for timeout/retry logic.
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					bodyBytes, _ := io.ReadAll(resp.Body)
					return fmt.Errorf("ollama API returned status %d: %s", resp.StatusCode, string(bodyBytes))
				}

				// Decouple I/O to allow concurrent user input handling.
				linesCh := make(chan []byte)
				errCh := make(chan error, 1)
				go func() {
					defer close(linesCh)
					scanner := bufio.NewScanner(resp.Body)
					for scanner.Scan() {
						lineCopy := make([]byte, len(scanner.Bytes()))
						copy(lineCopy, scanner.Bytes())
						linesCh <- lineCopy
					}
					errCh <- scanner.Err()
				}()

			processingLoop:
				for {
					select {
					case line, ok := <-linesCh:
						if !ok {
							break processingLoop // Stream finished.
						}
						var msg OllamaResponse
						if err := json.Unmarshal(line, &msg); err != nil {
							log.Printf("Ignoring non-JSON line from Ollama API: %s", string(line))
							continue
						}
						if msg.Status == "success" {
							downloadFinished = true
						}
						progressCh <- ProgressMsg{
							Status:    msg.Status,
							Completed: msg.Completed,
							Total:     msg.Total,
						}
					case choice := <-userChoiceCh:
						if choice == "Quit" {
							log.Println("User chose to quit during download.")
							return errors.New("user quit")
						}
					case <-ctx.Done():
						log.Println("Main context cancelled during stream reading.")
						return ctx.Err()
					}
				}

				if err := <-errCh; err != nil {
					return fmt.Errorf("error reading response stream: %w", err)
				}
				return nil
			}()

			if err != nil {
				// Handle errors from the download attempt.
				if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "user quit") {
					log.Println("Exiting due to cancellation or user quit.")
					return
				}

				isTimeout := errors.Is(err, context.DeadlineExceeded) || (func() bool {
					netErr, ok := err.(net.Error)
					return ok && netErr.Timeout()
				}())

				if isTimeout {
					log.Printf("Request timed out. continueUntilComplete: %t", continueUntilComplete)
					if continueUntilComplete {
						time.Sleep(1 * time.Second) // Shorter sleep for tests
						continue retryLoop
					}

					progressCh <- TimeoutMsg{}
					select {
					case choice := <-userChoiceCh:
						switch choice {
						case "Continue (until next error)", "Continue (until download completed)":
							if choice == "Continue (until download completed)" {
								continueUntilComplete = true
							}
							continue retryLoop
						case "Quit":
							return
						default:
							return
						}
					case <-ctx.Done():
						return
					}
				} else {
					// A different, non-timeout error occurred.
					progressCh <- ErrorMsg{Err: err}
					return
				}
			}

			if downloadFinished {
				log.Println("Download finished successfully.")
				return
			}

			// If we get here, the stream ended but not with a "success" message.
			if continueUntilComplete {
				time.Sleep(1 * time.Second)
				continue retryLoop
			} else {
				progressCh <- ErrorMsg{Err: errors.New("download stream ended unexpectedly")}
				return
			}
		}
	}()
}
