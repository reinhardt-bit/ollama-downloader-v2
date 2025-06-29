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

		pullReq := PullRequest{
			Model:  model,
			Stream: true,
		}

		body, err := json.Marshal(pullReq)
		if err != nil {
			log.Printf("Error marshalling request: %v", err)
			progressCh <- ErrorMsg{Err: fmt.Errorf("error marshalling request: %w", err)}
			close(progressCh)
			return
		}

		client := &http.Client{}

		var downloadFinished bool

		for {
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
				reqCancel()
				log.Printf("Error creating request: %v", err)
				progressCh <- ErrorMsg{Err: fmt.Errorf("error creating request: %w", err)}
				close(progressCh)
				return
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				reqCancel()
				isTimeout := errors.Is(err, context.DeadlineExceeded) || (func() bool {
					netErr, ok := err.(net.Error)
					return ok && netErr.Timeout()
				}())

				if isTimeout {
					log.Printf("Request timed out: context deadline exceeded or net error. continueUntilComplete: %t", continueUntilComplete)
					if continueUntilComplete {
						log.Println("Retrying download in 5 seconds...")
						time.Sleep(5 * time.Second)
						continue
					} else {
						progressCh <- TimeoutMsg{}
						select {
						case choice := <-userChoiceCh:
							switch choice {
							case "Continue (until next error)":
								log.Println("User chose to continue (single retry).")
								continue
							case "Continue (until download completed)":
								log.Println("User chose to continue (until complete).")
								continueUntilComplete = true
								continue
							case "Quit":
								log.Println("User chose to quit.")
								close(progressCh)
								return
							default:
								log.Printf("Unknown user choice: %s. Quitting.", choice)
								close(progressCh)
								return
							}
						case <-ctx.Done():
							log.Println("Main context cancelled while waiting for user choice.")
							close(progressCh)
							return
						}
					}
				} else {
					log.Printf("Error pulling model: %v", err)
					progressCh <- ErrorMsg{Err: fmt.Errorf("error pulling model: %w", err)}
					close(progressCh)
					return
				}
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				reqCancel()
				bodyBytes, _ := io.ReadAll(resp.Body)
				log.Printf("Ollama API returned non-200 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
				progressCh <- ErrorMsg{Err: fmt.Errorf("ollama API returned status %d: %s", resp.StatusCode, string(bodyBytes))}
				close(progressCh)
				return
			}

			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				select {
				case <-ctx.Done():
					log.Println("Main context cancelled during stream reading.")
					reqCancel()
					if !continueUntilComplete {
						progressCh <- TimeoutMsg{}
						select {
						case choice := <-userChoiceCh:
							switch choice {
							case "Continue (until next error)":
								log.Println("User chose to continue (single retry).")
								continue
							case "Continue (until download completed)":
								log.Println("User chose to continue (until complete).")
								continueUntilComplete = true
								continue
							case "Quit":
								log.Println("User chose to quit.")
								close(progressCh)
								return
							default:
								log.Printf("Unknown user choice: %s. Quitting.", choice)
								close(progressCh)
								return
							}
						case <-ctx.Done():
							log.Println("Main context cancelled while waiting for user choice.")
							close(progressCh)
							return
						}
					} else {
						log.Println("Main context cancelled during stream reading (auto-retry). Retrying...")
						reqCancel()
						time.Sleep(5 * time.Second)
						continue
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

			reqCancel()

			if err := scanner.Err(); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					log.Println("Stream reading timed out: context deadline exceeded")
					if continueUntilComplete {
						log.Println("Retrying download in 5 seconds...")
						time.Sleep(5 * time.Second)
						continue
					} else {
						progressCh <- TimeoutMsg{}
						select {
						case choice := <-userChoiceCh:
							switch choice {
							case "Continue (until next error)":
								log.Println("User chose to continue (single retry).")
								continue
							case "Continue (until download completed)":
								log.Println("User chose to continue (until complete).")
								continueUntilComplete = true
								continue
							case "Quit":
								log.Println("User chose to quit.")
								close(progressCh)
								return
							default:
								log.Printf("Unknown user choice: %s. Quitting.", choice)
								close(progressCh)
								return
							}
						case <-ctx.Done():
							log.Println("Main context cancelled while waiting for user choice.")
							close(progressCh)
							return
						}
					}
				} else {
					log.Printf("Error reading response: %v", err)
					progressCh <- ErrorMsg{Err: fmt.Errorf("error reading response: %w", err)}
					close(progressCh)
					return
				}
			}
			if downloadFinished {
				break
			} else if !continueUntilComplete {
				progressCh <- TimeoutMsg{}
				select {
				case choice := <-userChoiceCh:
					switch choice {
					case "Continue (until next error)":
						log.Println("User chose to continue (single retry).")
						continue
					case "Continue (until download completed)":
						log.Println("User chose to continue (until complete).")
						continueUntilComplete = true
						continue
					case "Quit":
						log.Println("User chose to quit.")
						close(progressCh)
						return
					default:
						log.Printf("Unknown user choice: %s. Quitting.", choice)
						close(progressCh)
						return
					}
				case <-ctx.Done():
					log.Println("Main context cancelled while waiting for user choice.")
					close(progressCh)
					return
				}
			}
		}
		if downloadFinished {
			log.Println("Download finished successfully, closing progress channel.")
			close(progressCh)
		}
	}()
}
