# Ollama Downloader Plan

**Project Goal:** Create a standalone, command-line tool that downloads Ollama models with a rich, interactive progress bar and robust error handling.

**Core Features:**

1.  **Direct API Interaction:** The tool will communicate directly with the Ollama `/api/pull` endpoint, giving us full control over the download process. See the [Ollama Pull API documentation](https://github.com/ollama/ollama/blob/main/docs/api.md#pull-a-model) for more details.
2.  **Interactive Progress Bar:** A Bubble Tea interface will provide a smooth, visually appealing progress bar that shows the download percentage, downloaded size, total size, speed, and ETA.
3.  **Graceful Error Handling:** The tool will handle network errors, API errors, and other issues gracefully, providing clear feedback to the user, including for invalid model names.
4.  **Timeout and Resume:** If a download times out or a context deadline is exceeded, the program will not crash. Instead, the user will be presented with the following choices:
    *   **1. Continue:** Resume the download. If another timeout or context deadline error occurs, the choice will be presented again. This allows for a loop with human intervention.
    *   **2. Continue until download completed:** Resume the download and automatically continue without further prompts, even if more timeout or context deadline errors occur, until the download is complete.
    *   **3. Quit:** Terminate the program.
5.  **Resumable Downloads:** The tool will leverage Ollama's built-in resume functionality to continue interrupted downloads.
6.  **Graceful Cancellation:** Users can cancel the download at any point.

**Command-Line Interface:**

*   **Usage:** `ollama-downloader --model <model-name>`
*   **Flags:**
    *   `--model, -m`: (Required) The name of the model to download (e.g., "llama3").
    *   `--host`: (Optional) The Ollama API host and port (e.g., "http://localhost:11434"). Defaults to the value of the `OLLAMA_HOST` environment variable or `http://localhost:11434` if not set.
    *   `--help, -h`: Displays the help message.

**Configuration:**

*   The Ollama host can be configured via the `--host` command-line flag or the `OLLAMA_HOST` environment variable. The flag takes precedence.

**Technical Stack:**

*   **Language:** Go
*   **Libraries:**
    *   `net/http` for making API requests.
    *   `context` for managing request cancellation.
    *   `encoding/json` for parsing JSON responses.
    *   `github.com/charmbracelet/bubbletea` for the interactive UI.
    *   `github.com/charmbracelet/bubbles/progress` for the progress bar.
    *   `github.com/charmbracelet/lipgloss` for styling.

**Development Plan:**


1.  **Project Setup:**
    *   Create a new Go module.
    *   Add the necessary dependencies to `go.mod`.
2.  **API Client (`client` package):**
    *   Implement a `PullModel` function that sends a POST request to the `/api/pull` endpoint.
    *   The function will accept a `context.Context` for graceful cancellation.
    *   It will take the model name, host, and a channel for progress updates as input.
    *   It will read the streaming JSON response line-by-line, parsing each line into a status struct.
    *   It will send `progressMsg` messages to the UI channel.
    *   Implement timeout detection. If a timeout occurs, send a `timeoutMsg` to the UI.
3.  **Bubble Tea UI (`ui` package):**
    *   Define the `model` struct to hold the application state (progress, error, timeout status, etc.).
    *   Implement the `Init`, `Update`, and `View` methods.
    *   The `Update` method will handle `progressMsg`, `errorMsg`, and `timeoutMsg` messages.
    *   On `timeoutMsg`, it will update the view to show "Continue" and "Cancel" options.
    *   Handle user input for continuing or canceling a timed-out download.
    *   The `View` method will render the progress bar, status messages, and other UI elements.
4.  **Main Application (`main.go`):**
    *   Parse command-line flags (`--model`, `--host`, `--help`).
    *   Handle the `OLLAMA_HOST` environment variable.
    *   Create a `context.Context` to manage the application lifecycle.
    *   Create and run the Bubble Tea `tea.Program`.
    *   Call the `PullModel` function in a separate goroutine to start the download.
5.  **Testing:**
    *   Develop unit tests for the `client` package.
    *   Use a mock HTTP server to simulate the Ollama API responses (success, error, streaming data, and timeouts).
    *   Verify that the JSON parsing and error handling logic is correct.
