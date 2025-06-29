package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"ollama-downloader-v2/client"
	"ollama-downloader-v2/ui"
)

func main() {
	logFile, err := os.OpenFile("ollama-downloader.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)

	var modelName string
	var host string

	flag.StringVar(&modelName, "model", "", "The name of the model to download (e.g., 'llama3')")
	flag.StringVar(&modelName, "m", "", "The name of the model to download (shorthand)")
	flag.StringVar(&host, "host", "", "Ollama API host (e.g., 'http://localhost:11434'). Overrides OLLAMA_HOST.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s -model <model-name> [flags]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if modelName == "" {
		log.Println("Error: model name is required.")
		fmt.Println("Error: model name is required.")
		flag.Usage()
		os.Exit(1)
	}

	if host == "" {
		host = os.Getenv("OLLAMA_HOST")
		if host == "" {
			host = "http://localhost:11434"
		}
	}

	var continueUntilComplete bool
	var shouldQuit bool

	for {
		if shouldQuit {
			break
		}

		log.Printf("Starting download for model: %s from host: %s", modelName, host)

		// Create a new context for each download attempt
		ctx, cancel := context.WithCancel(context.Background())
		// The cancel function will be called when the UI exits or when the loop iteration finishes.
		// This ensures the PullModel goroutine's context is cancelled.

		progressCh := make(chan tea.Msg)
		// Channel to signal the UI to quit from the PullModel goroutine
		quitUICh := make(chan struct{})
		// Channel to send user's choice from UI to PullModel
		userChoiceCh := make(chan string) // Unbuffered channel

		model := ui.NewModel(modelName, host, cancel, quitUICh, userChoiceCh) // Pass userChoiceCh to UI
		p := tea.NewProgram(model)

		// Start PullModel in a goroutine
		go client.PullModel(ctx, modelName, host, progressCh, continueUntilComplete, userChoiceCh)

		// Goroutine to send messages from client to UI
		go func() {
			for msg := range progressCh {
				p.Send(msg)
			}
			// When progressCh is closed (meaning PullModel has finished),
			// signal the UI to quit if it hasn't already.
			select {
			case <-quitUICh: // UI already quit
			default:
				p.Send(tea.Quit())
			}
		}()

		// Run the Bubble Tea program
		finalModel, err := p.Run()
		if err != nil {
			// If the error is context cancellation/timeout, it means the UI was dismissed
			// and we should proceed to process the selected choice.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Program exited due to context cancellation/timeout: %v\n", err)
			} else {
				log.Printf("Alas, there's been an error: %v\n", err)
				fmt.Printf("Alas, there's been an error: %v\n", err)
				os.Exit(1)
			}
		}

		// Ensure the context for the current attempt is cancelled
		cancel()

		appModel := finalModel.(ui.Model)
		selectedChoice := appModel.GetSelectedChoice()

		switch selectedChoice {
		case "Continue (until next error)":
			continueUntilComplete = false
			log.Println("Continuing download (single retry)...\n")
			// No need to `continue` here, the loop will naturally continue.
		case "Continue (until download completed)":
			continueUntilComplete = true
			log.Println("Continuing download (until complete)....\n")
			// No need to `continue` here, the loop will naturally continue.
		case "Quit":
			log.Println("Quitting download.\n")
			shouldQuit = true
		default:
			// This case is hit if the download completes successfully or if an unknown choice is made.
			// If continueUntilComplete is true, it means the download finished successfully
			// and we should exit the loop.
			if continueUntilComplete {
				log.Println("Download completed successfully.\n")
				shouldQuit = true
			} else {
				log.Println("Download finished or unknown choice, quitting.\n")
				shouldQuit = true
			}
		}
		// Add a small delay to allow goroutines to clean up before next iteration
		time.Sleep(100 * time.Millisecond)
	}

	log.Println("Download finished.")
}