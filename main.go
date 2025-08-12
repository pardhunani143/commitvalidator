package main

import (
    "encoding/json"
    "fmt"
    "io/ioutil"
    "log"
    "net/http"
    "os"
    "bytes"
)

func prWebhookHandler(w http.ResponseWriter, r *http.Request) {
    var payload []byte
    if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
        // Parse form and get the payload field
        if err := r.ParseForm(); err != nil {
            http.Error(w, "Could not parse form", http.StatusBadRequest)
            return
        }
        payloadStr := r.FormValue("payload")
        payload = []byte(payloadStr)
    } else {
        // Assume JSON
        var err error
        payload, err = ioutil.ReadAll(r.Body)
        if err != nil {
            http.Error(w, "Could not read request body", http.StatusInternalServerError)
            return
        }
    }

    // Parse the webhook payload
    var prEvent struct {
        Action string `json:"action"`
        Number int    `json:"number"`
        PullRequest struct {
            Number int `json:"number"`
        } `json:"pull_request"`
        Repository struct {
            Name  string `json:"name"`
            Owner struct {
                Login string `json:"login"`
            } `json:"owner"`
        } `json:"repository"`
    }
    if err := json.Unmarshal(payload, &prEvent); err != nil {
        log.Printf("Could not parse PR event: %v", err)
        log.Printf("Raw payload: %s", string(payload))
        fmt.Fprintf(w, "Webhook received, but could not parse PR event")
        return
    }

    // Only handle PR events with action 'opened'
    if prEvent.Action != "opened" {
        log.Printf("Ignoring PR event with action: %s", prEvent.Action)
        fmt.Fprintf(w, "Ignoring PR event with action: %s", prEvent.Action)
        return
    }

    prNumber := prEvent.PullRequest.Number
    if prNumber == 0 {
        prNumber = prEvent.Number
    }
    if prNumber == 0 {
        log.Printf("No PR number found in event")
        fmt.Fprintf(w, "No PR number found")
        return
    }

    owner := prEvent.Repository.Owner.Login
    repo := prEvent.Repository.Name
    log.Printf("PR #%d opened for repo %s/%s", prNumber, owner, repo)

    // Fetch changed files from GitHub API
    files, err := fetchPRFiles(owner, repo, prNumber)
    if err != nil {
        log.Printf("Error fetching PR files: %v", err)
        fmt.Fprintf(w, "Error fetching PR files")
        return
    }
    log.Printf("Changed files in PR #%d:", prNumber)
    for _, f := range files {
        log.Printf("- %s (additions: %d, deletions: %d, changes: %d)", f.Filename, f.Additions, f.Deletions, f.Changes)
    }

    // --- PR Validation Logic ---
    validationPassed := validatePR(files)
    status := "success"
    description := "PR validation passed."
    if !validationPassed {
        status = "failure"
        description = "PR validation failed."
    }
    // Update PR status on GitHub (optional: close PR if failed)
    err = updatePRStatus(owner, repo, prNumber, status, description)
    if err != nil {
        log.Printf("Error updating PR status: %v", err)
    }
    // Optionally close PR if validation failed
    if !validationPassed {
        err = closePullRequest(owner, repo, prNumber)
        if err != nil {
            log.Printf("Error closing PR: %v", err)
        } else {
            log.Printf("PR #%d closed due to failed validation.", prNumber)
        }
    }
    fmt.Fprintf(w, "PR #%d validation complete. Status: %s\n", prNumber, status)
    fmt.Fprintf(w, "Files changed in PR:\n")
    for _, f := range files {
        fmt.Fprintf(w, "- %s (additions: %d, deletions: %d, changes: %d)\n", f.Filename, f.Additions, f.Deletions, f.Changes)
    }
}

// validatePR runs custom validation logic on PR files
func validatePR(files []PRFile) bool {
    // TODO: Add your validation logic here
    // Example: return false if any file is named "forbidden.txt"
    for _, f := range files {
        if f.Filename == "forbidden.txt" {
            return false
        }
    }
    return false
}

// updatePRStatus posts a status to the PR using the GitHub API
func updatePRStatus(owner, repo string, prNumber int, state, description string) error {
    // Get PR details to find the head SHA
    prURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
    token := os.Getenv("GITHUB_TOKEN")
    req, err := http.NewRequest("GET", prURL, nil)
    if err != nil {
        return err
    }
    req.Header.Set("Authorization", "token "+token)
    req.Header.Set("Accept", "application/vnd.github.v3+json")
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != 200 {
        body, _ := ioutil.ReadAll(resp.Body)
        return fmt.Errorf("GitHub API error: %s", string(body))
    }
    var prData struct {
        Head struct {
            SHA string `json:"sha"`
        } `json:"head"`
    }
    decoder := json.NewDecoder(resp.Body)
    if err := decoder.Decode(&prData); err != nil {
        return err
    }
    sha := prData.Head.SHA
    // Set status on the commit
    statusURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/statuses/%s", owner, repo, sha)
    statusBody := map[string]string{
        "state": state,
        "description": description,
        "context": "commitvalidator",
    }
    bodyBytes, _ := json.Marshal(statusBody)
    req, err = http.NewRequest("POST", statusURL, bytes.NewBuffer(bodyBytes))
    if err != nil {
        return err
    }
    req.Header.Set("Authorization", "token "+token)
    req.Header.Set("Accept", "application/vnd.github.v3+json")
    req.Header.Set("Content-Type", "application/json")
    resp, err = client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != 201 {
        body, _ := ioutil.ReadAll(resp.Body)
        return fmt.Errorf("GitHub API error: %s", string(body))
    }
    log.Printf("PR #%d [%s/%s] status updated to %s: %s", prNumber, owner, repo, state, description)
    return nil
}

// closePullRequest closes the PR using the GitHub API
func closePullRequest(owner, repo string, prNumber int) error {
    token := os.Getenv("GITHUB_TOKEN")
    url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
    body := map[string]string{"state": "closed"}
    bodyBytes, _ := json.Marshal(body)
    req, err := http.NewRequest("PATCH", url, bytes.NewBuffer(bodyBytes))
    if err != nil {
        return err
    }
    req.Header.Set("Authorization", "token "+token)
    req.Header.Set("Accept", "application/vnd.github.v3+json")
    req.Header.Set("Content-Type", "application/json")
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != 200 {
        body, _ := ioutil.ReadAll(resp.Body)
        return fmt.Errorf("GitHub API error: %s", string(body))
    }
    log.Printf("PR #%d [%s/%s] has been closed after validation.", prNumber, owner, repo)
    return nil
}

// PRFile represents a file changed in a PR
type PRFile struct {
    Filename  string `json:"filename"`
    Additions int    `json:"additions"`
    Deletions int    `json:"deletions"`
    Changes   int    `json:"changes"`
    Status    string `json:"status"`
    RawURL    string `json:"raw_url"`
    BlobURL   string `json:"blob_url"`
    Patch     string `json:"patch"`
}

// fetchPRFiles gets the list of changed files for a PR from GitHub
func fetchPRFiles(owner, repo string, prNumber int) ([]PRFile, error) {
    url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/files", owner, repo, prNumber)
    req, err := http.NewRequest("GET", url, nil)
    if err != nil {
        return nil, err
    }
    // Optionally set a GitHub token for private repos or higher rate limits
    // token := os.Getenv("GITHUB_TOKEN")
    // if token != "" {
    //     req.Header.Set("Authorization", "token "+token)
    // }
    req.Header.Set("Accept", "application/vnd.github.v3+json")
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode != 200 {
        body, _ := ioutil.ReadAll(resp.Body)
        return nil, fmt.Errorf("GitHub API error: %s", string(body))
    }
    var files []PRFile
    decoder := json.NewDecoder(resp.Body)
    if err := decoder.Decode(&files); err != nil {
        return nil, err
    }
    return files, nil
}

func main() {
    http.HandleFunc("/webhook", prWebhookHandler)
    port := "8080"
    log.Printf("Server listening on port %s", port)
    log.Fatal(http.ListenAndServe(":"+port, nil))
}