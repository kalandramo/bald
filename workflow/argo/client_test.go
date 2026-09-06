package argo

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeArgo 模拟 Argo Server 的最小 REST 面。
type fakeArgo struct {
	mu        sync.Mutex
	workflows map[string]*Workflow
	auth      string // 收到的 Authorization 头
	lastBody  string
}

func newFakeArgo(t *testing.T) (*fakeArgo, *httptest.Server) {
	t.Helper()
	f := &fakeArgo{workflows: map[string]*Workflow{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/workflows/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.auth = r.Header.Get("Authorization")
		f.lastBody = string(body)
		defer f.mu.Unlock()

		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/workflows/")
		parts := strings.Split(rest, "/")

		switch r.Method {
		case http.MethodPost: // submit: /{namespace}
			name := "hello-world"
			if strings.Contains(f.lastBody, "generateName") {
				name = "gen-wf-1"
			}
			wf := &Workflow{Metadata: ObjectMeta{Name: name, Namespace: parts[0]}}
			f.workflows[name] = wf
			writeJSON(w, wf)
		case http.MethodGet:
			if len(parts) == 2 { // get: /{namespace}/{name}
				wf, ok := f.workflows[parts[1]]
				if !ok {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				writeJSON(w, wf)
				return
			}
			// list: /{namespace}
			items := make([]Workflow, 0, len(f.workflows))
			for _, wf := range f.workflows {
				items = append(items, *wf)
			}
			writeJSON(w, &WorkflowList{Items: items})
		case http.MethodDelete:
			delete(f.workflows, parts[1])
			w.WriteHeader(http.StatusOK)
		case http.MethodPut: // lifecycle: suspend/resume/terminate/resubmit/retry/stop
			wf, ok := f.workflows[parts[1]]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			switch parts[2] {
			case "suspend", "resume", "terminate":
				wf.Status = &WorkflowStatus{Phase: PhaseRunning}
				w.WriteHeader(http.StatusOK)
			case "stop":
				wf.Status = &WorkflowStatus{Phase: PhaseFailed, Message: f.lastBody}
				w.WriteHeader(http.StatusOK)
			case "resubmit", "retry":
				writeJSON(w, wf)
			default:
				http.Error(w, "unknown action", http.StatusNotFound)
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	b := []byte(`{"metadata":{"name":"hello-world","namespace":"default"},"status":{"phase":"Succeeded"}}`)
	if _, ok := v.(*WorkflowList); ok {
		b = []byte(`{"items":[{"metadata":{"name":"hello-world"}}]}`)
	}
	_, _ = w.Write(b)
}

func TestWorkflowLifecycle(t *testing.T) {
	_, srv := newFakeArgo(t)
	client, err := NewClient(ClientOptions{ServerURL: srv.URL, Namespace: "ci", Token: "tok-123"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	ctx := context.Background()

	submitted, err := client.SubmitWorkflow(ctx, &Workflow{
		APIVersion: apiVersion,
		Kind:       "Workflow",
		Spec:       WorkflowSpec{Entrypoint: "main"},
	}, &SubmitOptions{Parameters: []string{"msg=hello"}})
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if submitted.Metadata.Name == "" {
		t.Fatal("submitted workflow has no name")
	}

	got, err := client.GetWorkflow(ctx, submitted.Metadata.Name, "")
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if got.Metadata.Name != submitted.Metadata.Name {
		t.Fatalf("GetWorkflow name = %q, want %q", got.Metadata.Name, submitted.Metadata.Name)
	}

	list, err := client.ListWorkflows(ctx, &ListOptions{LabelSelector: "app=ci"})
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(list.Items) == 0 {
		t.Fatal("ListWorkflows returned no items")
	}

	for _, step := range []struct {
		name string
		fn   func() error
	}{
		{"Suspend", func() error { return client.SuspendWorkflow(ctx, submitted.Metadata.Name, "") }},
		{"Resume", func() error { return client.ResumeWorkflow(ctx, submitted.Metadata.Name, "") }},
		{"Terminate", func() error { return client.TerminateWorkflow(ctx, submitted.Metadata.Name, "") }},
		{"Stop", func() error { return client.StopWorkflow(ctx, submitted.Metadata.Name, "", "manual stop") }},
	} {
		if err := step.fn(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}

	resub, err := client.ResubmitWorkflow(ctx, submitted.Metadata.Name, "")
	if err != nil {
		t.Fatalf("ResubmitWorkflow: %v", err)
	}
	if resub == nil {
		t.Fatal("ResubmitWorkflow returned nil")
	}

	retried, err := client.RetryWorkflow(ctx, submitted.Metadata.Name, "")
	if err != nil {
		t.Fatalf("RetryWorkflow: %v", err)
	}
	if retried == nil {
		t.Fatal("RetryWorkflow returned nil")
	}

	if err := client.DeleteWorkflow(ctx, submitted.Metadata.Name, ""); err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}

	if _, err := client.GetWorkflow(ctx, submitted.Metadata.Name, ""); err == nil {
		t.Fatal("GetWorkflow after delete should fail")
	}
}

func TestNewRequestAuth(t *testing.T) {
	f, srv := newFakeArgo(t)
	client, err := NewClient(ClientOptions{ServerURL: srv.URL, Token: "tok-abc"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	if _, err := client.GetWorkflow(context.Background(), "any", "ns1"); err == nil {
		// fake 里没有该 workflow，404 属预期；这里只验证请求已带认证头。
	}
	if f.auth != "Bearer tok-abc" {
		t.Fatalf("Authorization = %q, want %q", f.auth, "Bearer tok-abc")
	}
}

func TestErrorPropagation(t *testing.T) {
	_, srv := newFakeArgo(t)
	client, err := NewClient(ClientOptions{ServerURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	_, err = client.GetWorkflow(context.Background(), "missing", "ns1")
	if err == nil {
		t.Fatal("expected error for missing workflow")
	}
	if !strings.Contains(err.Error(), "get workflow") {
		t.Fatalf("error should identify operation, got: %v", err)
	}
}

func TestPhaseIsTerminal(t *testing.T) {
	for _, p := range []Phase{PhaseSucceeded, PhaseFailed, PhaseError} {
		if !p.IsTerminal() {
			t.Errorf("Phase %q should be terminal", p)
		}
	}
	for _, p := range []Phase{PhasePending, PhaseRunning} {
		if p.IsTerminal() {
			t.Errorf("Phase %q should not be terminal", p)
		}
	}
}
