package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"
	"errors"

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

// PullModel pulls an Ollama model with progress updates and timeout handling.
// It now accepts a userChoiceCh to receive the user's decision after a timeout.
func PullModel(ctx context.Context, model string, host string, progressCh chan<- tea.Msg, continueUntilComplete bool, userChoiceCh <-chan string) {
	go func() {
		// IMPORTANT: progressCh is NOT deferred closed here. It is closed explicitly
		// when the PullModel goroutine is truly finished (success, unrecoverable error, or explicit user quit).

		pullReq := PullRequest{
			Model:  model,
			Stream: true,
		}

		body, err := json.Marshal(pullReq)
		if err != nil {
			log.Printf("Error marshalling request: %v", err)
			progressCh <- ErrorMsg{Err: fmt.Errorf("error marshalling request: %w", err)}
			close(progressCh) // Close on unrecoverable error
			return
		}

		client := &http.Client{}

		var downloadFinished bool

		for { // Retry loop
			// Always check for context cancellation or user quit at the beginning of the loop
			select {
			case <-ctx.Done():
				log.Println("Main context cancelled during PullModel (top of loop).")
				close(progressCh)
				return
			case choice := <-userChoiceCh:
				if choice == "Quit" {
					log.Println("User chose to quit (top of loop).")
					close(progressCh)
					return
				}
			default:
				// Continue
			}

			reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Second) // 30 second timeout for the request
			req, err := http.NewRequestWithContext(reqCtx, "POST", host+"/api/pull", bytes.NewBuffer(body))
			if err != nil {
				reqCancel() // Cancel immediately if request creation fails
				log.Printf("Error creating request: %v", err)
				progressCh <- ErrorMsg{Err: fmt.Errorf("error creating request: %w", err)}
				close(progressCh) // Close on unrecoverable error
				return
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				reqCancel() // Cancel immediately if client.Do fails
				isTimeout := errors.Is(err, context.DeadlineExceeded) || (func() bool {
					netErr, ok := err.(net.Error)
					return ok && netErr.Timeout()
				}())

				if isTimeout {
					log.Printf("Request timed out: context deadline exceeded or net error. continueUntilComplete: %t", continueUntilComplete)
					if continueUntilComplete {
						log.Println("Retrying download in 5 seconds...")
						time.Sleep(5 * time.Second)
						continue // Retry the request
					} else {
						progressCh <- TimeoutMsg{}
						// Wait for user choice from UI
						select {
						case choice := <-userChoiceCh:
							switch choice {
							case "Continue (until next error)":
								log.Println("User chose to continue (single retry).")
								continue // Continue the retry loop
							case "Continue (until download completed)":
								log.Println("User chose to continue (until complete).")
								continueUntilComplete = true
								continue // Continue the retry loop
							case "Quit":
								log.Println("User chose to quit.")
								close(progressCh) // Close if user quits
								return // Exit PullModel goroutine
							default:
								log.Printf("Unknown user choice: %s. Quitting.", choice)
								close(progressCh) // Close on unknown choice
								return // Exit PullModel goroutine
							}
						case <-ctx.Done():
							log.Println("Main context cancelled while waiting for user choice.")
							close(progressCh) // Close if main context is cancelled
							return // Exit PullModel goroutine
						}
					}
				} else {
					log.Printf("Error pulling model: %v", err)
					progressCh <- ErrorMsg{Err: fmt.Errorf("error pulling model: %w", err)}
					close(progressCh) // Close on unrecoverable error
					return
				}
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				reqCancel() // Cancel if non-200 status
				bodyBytes, _ := io.ReadAll(resp.Body)
				log.Printf("Ollama API returned non-200 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
				progressCh <- ErrorMsg{Err: fmt.Errorf("ollama API returned status %d: %s", resp.StatusCode, string(bodyBytes))}
				close(progressCh) // Close on unrecoverable error
				return
			}

			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				select {
				case <-ctx.Done():
					log.Println("Main context cancelled during stream reading.")
					// If the main context is cancelled, we should also cancel the request context
					reqCancel()
					if !continueUntilComplete { // Only show menu if not continuing until complete
						progressCh <- TimeoutMsg{}
						// Wait for user choice from UI
						select {
						case choice := <-userChoiceCh:
							switch choice {
							case "Continue (until next error)":
								log.Println("User chose to continue (single retry).")
								continue // Continue the retry loop
							case "Continue (until download completed)":
								log.Println("User chose to continue (until complete).")
								continueUntilComplete = true
								continue // Continue the retry loop
							case "Quit":
								log.Println("User chose to quit.")
								close(progressCh) // Close if user quits
								return // Exit PullModel goroutine
							default:
								log.Printf("Unknown user choice: %s. Quitting.", choice)
								close(progressCh) // Close on unknown choice
								return // Exit PullModel goroutine
							}
						case <-ctx.Done():
							log.Println("Main context cancelled while waiting for user choice.")
							close(progressCh) // Close if main context is cancelled
							return // Exit PullModel goroutine
						}
					} else { // If continueUntilComplete is true, just continue the retry loop
						log.Println("Main context cancelled during stream reading (auto-retry). Retrying...")
						reqCancel() // Cancel the request context
						time.Sleep(5 * time.Second)
						continue // Continue the retry loop
					}
				default:
					var msg OllamaResponse
					if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
						log.Printf("Ignoring non-JSON line from Ollama API: %s", scanner.Text())
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
				}
			}

			reqCancel() // Cancel the request context after reading the response body

			if err := scanner.Err(); err != nil {
				// Check if the scanner error is due to the request context timing out
				if errors.Is(err, context.DeadlineExceeded) {
					log.Println("Stream reading timed out: context deadline exceeded")
					if continueUntilComplete {
						log.Println("Retrying download in 5 seconds...")
						time.Sleep(5 * time.Second)
						continue // Retry the request
					} else {
						progressCh <- TimeoutMsg{}
						// Wait for user choice from UI
						select {
						case choice := <-userChoiceCh:
							switch choice {
							case "Continue (until next error)":
								log.Println("User chose to continue (single retry).")
								continue // Continue the retry loop
							case "Continue (until download completed)":
								log.Println("User chose to continue (until complete).")
								continueUntilComplete = true
								continue // Continue the retry loop
							case "Quit":
								log.Println("User chose to quit.")
								close(progressCh) // Close if user quits
								return // Exit PullModel goroutine
							default:
								log.Printf("Unknown user choice: %s. Quitting.", choice)
								close(progressCh) // Close on unknown choice
								return // Exit PullModel goroutine
							}
						case <-ctx.Done():
							log.Println("Main context cancelled while waiting for user choice.")
							close(progressCh) // Close if main context is cancelled
							return // Exit PullModel goroutine
						}
					}
				} else {
					log.Printf("Error reading response: %v", err)
					progressCh <- ErrorMsg{Err: fmt.Errorf("error reading response: %w", err)}
					close(progressCh) // Close on unrecoverable error
					return
				}
			}
			// If we reach here, the download stream completed without a timeout error.
			// This means the pull was successful for this segment.
			// Break the retry loop if the download is finished.
			if downloadFinished {
				break
			} else if !continueUntilComplete {
				// If not finished and not continuing until complete, it means the stream ended
				// prematurely without a success message, so we should consider it a timeout.
				progressCh <- TimeoutMsg{}
				// Wait for user choice from UI
				select {
				case choice := <-userChoiceCh:
					switch choice {
					case "Continue (until next error)":
						log.Println("User chose to continue (single retry).")
						continue // Continue the retry loop
					case "Continue (until download completed)":
						log.Println("User chose to continue (until complete).")
						continueUntilComplete = true
						continue // Continue the retry loop
					case "Quit":
						log.Println("User chose to quit.")
						close(progressCh) // Close if user quits
						return // Exit PullModel goroutine
					default:
						log.Printf("Unknown user choice: %s. Quitting.", choice)
						close(progressCh) // Close on unknown choice
						return // Exit PullModel goroutine
					}
				case <-ctx.Done():
					log.Println("Main context cancelled while waiting for user choice.")
					close(progressCh) // Close if main context is cancelled
					return // Exit PullModel goroutine
				}
			}
			// If continueUntilComplete is true and downloadFinished is false, continue the loop
			// to retry the download.
		}
		// If the loop breaks (downloadFinished is true), close the channel.
		if downloadFinished {
			log.Println("Download finished successfully, closing progress channel.")
			close(progressCh)
		}
	}()
}