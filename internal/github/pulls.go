package github

// PRs, reviews e checks (Fase 4). Endpoints (API version 2022-11-28):
//   - POST  /repos/{o}/{r}/pulls                      https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request
//   - GET   /repos/{o}/{r}/pulls?head=o:branch        https://docs.github.com/en/rest/pulls/pulls#list-pull-requests
//   - GET   /repos/{o}/{r}/pulls/{n}                  https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request
//   - PATCH /repos/{o}/{r}/pulls/{n}                  https://docs.github.com/en/rest/pulls/pulls#update-a-pull-request
//   - POST  /repos/{o}/{r}/pulls/{n}/reviews          https://docs.github.com/en/rest/pulls/reviews#create-a-review-for-a-pull-request
//   - GET   /repos/{o}/{r}/pulls/{n}/reviews          https://docs.github.com/en/rest/pulls/reviews#list-reviews-for-a-pull-request
//   - GET   /repos/{o}/{r}/pulls/{n}/comments         https://docs.github.com/en/rest/pulls/comments#list-review-comments-on-a-pull-request
//   - GET   /repos/{o}/{r}/commits/{sha}/check-runs   https://docs.github.com/en/rest/checks/runs#list-check-runs-for-a-git-reference
//   - GET   /repos/{o}/{r}/check-runs/{id}/annotations https://docs.github.com/en/rest/checks/runs#list-check-run-annotations
//   - GET   /repos/{o}/{r}/commits?sha=&since=        https://docs.github.com/en/rest/commits/commits#list-commits
//   - GET   /repos/{o}/{r}/issues/{n}/events          https://docs.github.com/en/rest/issues/events#list-issue-events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PullRequest é o que o Sentinela usa de um PR.
type PullRequest struct {
	Number         int        `json:"number"`
	HTMLURL        string     `json:"html_url"`
	State          string     `json:"state"` // open | closed
	Draft          bool       `json:"draft"`
	Merged         bool       `json:"merged"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	// MergedBy é quem mergeou (só em PR mergeado).
	MergedBy *struct {
		Login string `json:"login"`
	} `json:"merged_by,omitempty"`
	Head struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
}

// NewPR é o corpo de criação; Draft é sempre true no Sentinela.
type NewPR struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
	Draft bool   `json:"draft"`
}

// ErrNotDraft protege a regra: o Sentinela só abre PR em draft.
var ErrNotDraft = errors.New("o Sentinela só abre PR em draft")

// CreatePR abre o PR. Recusa PR que não seja draft.
func (c *Client) CreatePR(ctx context.Context, repo string, pr NewPR) (PullRequest, error) {
	if !pr.Draft {
		return PullRequest{}, ErrNotDraft
	}
	var out PullRequest
	err := c.send(ctx, http.MethodPost, "/repos/"+repo+"/pulls", pr, &out)
	return out, err
}

// FindOpenPR procura o PR aberto da branch (owner:branch); nil se não houver.
func (c *Client) FindOpenPR(ctx context.Context, repo, owner, branch string) (*PullRequest, error) {
	var out []PullRequest
	q := url.Values{"head": {owner + ":" + branch}, "state": {"open"}}
	if err := c.send(ctx, http.MethodGet, "/repos/"+repo+"/pulls?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// GetPR lê um PR.
func (c *Client) GetPR(ctx context.Context, repo string, n int) (PullRequest, error) {
	var out PullRequest
	err := c.send(ctx, http.MethodGet, "/repos/"+repo+"/pulls/"+strconv.Itoa(n), nil, &out)
	return out, err
}

// IssueEvent é um evento da linha do tempo de uma issue ou PR (closed, reopened, merged...).
type IssueEvent struct {
	Event string `json:"event"`
	Actor *struct {
		Login string `json:"login"`
	} `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

// ClosedBy devolve o login de quem fechou o PR por último (evento closed); "" se não houver.
func (c *Client) ClosedBy(ctx context.Context, repo string, n int) (string, error) {
	var events []IssueEvent
	if err := c.send(ctx, http.MethodGet, "/repos/"+repo+"/issues/"+strconv.Itoa(n)+"/events?per_page=100", nil, &events); err != nil {
		return "", err
	}
	login := ""
	for _, e := range events {
		if e.Event == "closed" && e.Actor != nil {
			login = e.Actor.Login
		}
	}
	return login, nil
}

// UpdatePRBody troca o corpo do PR (rework e shepherd atualizam o resumo).
func (c *Client) UpdatePRBody(ctx context.Context, repo string, n int, body string) error {
	return c.send(ctx, http.MethodPatch, "/repos/"+repo+"/pulls/"+strconv.Itoa(n), map[string]string{"body": body}, nil)
}

// Review é uma review de PR.
type Review struct {
	ID          int64     `json:"id"`
	State       string    `json:"state"` // APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED
	Body        string    `json:"body"`
	SubmittedAt time.Time `json:"submitted_at"`
	User        struct {
		Login string `json:"login"`
		Type  string `json:"type"` // User | Bot
	} `json:"user"`
}

// Eventos de review aceitos pelo Sentinela.
const (
	ReviewEventComment = "COMMENT"
	ReviewEventApprove = "APPROVE"
)

// PostReview publica uma review. event é COMMENT ou APPROVE; quem decide se pode aprovar é o
// gate (internal/gate), nunca este cliente.
func (c *Client) PostReview(ctx context.Context, repo string, n int, event, body string) (Review, error) {
	if event != ReviewEventComment && event != ReviewEventApprove {
		return Review{}, fmt.Errorf("evento de review %q não suportado", event)
	}
	var out Review
	err := c.send(ctx, http.MethodPost, "/repos/"+repo+"/pulls/"+strconv.Itoa(n)+"/reviews", map[string]string{"event": event, "body": body}, &out)
	return out, err
}

// Reviews lista as reviews do PR.
func (c *Client) Reviews(ctx context.Context, repo string, n int) ([]Review, error) {
	var out []Review
	err := c.send(ctx, http.MethodGet, "/repos/"+repo+"/pulls/"+strconv.Itoa(n)+"/reviews?per_page=100", nil, &out)
	return out, err
}

// ReviewComment é um comentário de linha numa review.
type ReviewComment struct {
	PullRequestReviewID int64  `json:"pull_request_review_id"`
	Path                string `json:"path"`
	Line                int    `json:"line"`
	Body                string `json:"body"`
}

// ReviewComments lista os comentários de linha do PR.
func (c *Client) ReviewComments(ctx context.Context, repo string, n int) ([]ReviewComment, error) {
	var out []ReviewComment
	err := c.send(ctx, http.MethodGet, "/repos/"+repo+"/pulls/"+strconv.Itoa(n)+"/comments?per_page=100", nil, &out)
	return out, err
}

// CheckRun é um check do CI.
type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | neutral | cancelled | skipped | timed_out | action_required
	HTMLURL    string `json:"html_url"`
	Output     struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"output"`
}

// CheckRuns lista os checks de um commit.
func (c *Client) CheckRuns(ctx context.Context, repo, sha string) ([]CheckRun, error) {
	var out struct {
		CheckRuns []CheckRun `json:"check_runs"`
	}
	err := c.send(ctx, http.MethodGet, "/repos/"+repo+"/commits/"+sha+"/check-runs?per_page=100", nil, &out)
	return out.CheckRuns, err
}

// Annotation é uma anotação de um check (arquivo, linha, mensagem).
type Annotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	Level     string `json:"annotation_level"`
	Message   string `json:"message"`
}

// Annotations lista as anotações de um check.
func (c *Client) Annotations(ctx context.Context, repo string, checkID int64) ([]Annotation, error) {
	var out []Annotation
	err := c.send(ctx, http.MethodGet, "/repos/"+repo+"/check-runs/"+strconv.FormatInt(checkID, 10)+"/annotations?per_page=50", nil, &out)
	return out, err
}

// Commit é um commit da listagem.
type Commit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

// CommitsSince lista commits da branch desde um instante (detecção de revert).
func (c *Client) CommitsSince(ctx context.Context, repo, branch string, since time.Time) ([]Commit, error) {
	var out []Commit
	q := url.Values{"sha": {branch}, "since": {since.UTC().Format(time.RFC3339)}, "per_page": {"100"}}
	err := c.send(ctx, http.MethodGet, "/repos/"+repo+"/commits?"+q.Encode(), nil, &out)
	return out, err
}

// send faz uma chamada JSON autenticada com o token do cliente.
func (c *Client) send(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s %s", ErrNotFound, method, path)
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("github: %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(data), 300))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
