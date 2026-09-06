package argo

import "time"

////////////////////////////////////////////////////////////////////////////////
/// Client Options
////////////////////////////////////////////////////////////////////////////////

// ClientOptions configures the Argo Workflows API client.
type ClientOptions struct {
	// ServerURL is the Argo Server API URL (e.g. "https://localhost:2746").
	ServerURL string

	// Namespace is the Kubernetes namespace for workflow operations.
	Namespace string

	// Token is the Bearer token for authentication (optional).
	Token string

	// InsecureSkipVerify skips TLS certificate verification (for development).
	InsecureSkipVerify bool
}

////////////////////////////////////////////////////////////////////////////////
/// Submit Options
////////////////////////////////////////////////////////////////////////////////

// SubmitOptions holds options for submitting a workflow.
type SubmitOptions struct {
	// Namespace is the target namespace (overrides client default).
	Namespace string

	// ServerDryRun performs a dry run without creating the workflow.
	ServerDryRun bool

	// Parameters are workflow parameters in "key=value" format.
	Parameters []string
}

////////////////////////////////////////////////////////////////////////////////
/// List Options
////////////////////////////////////////////////////////////////////////////////

// ListOptions holds options for listing workflows.
type ListOptions struct {
	// Namespace is the target namespace (overrides client default).
	Namespace string

	// LabelSelector filters workflows by label.
	LabelSelector string

	// FieldSelector filters workflows by field.
	FieldSelector string

	// Limit restricts the number of results.
	Limit int64

	// Offset for pagination.
	Offset int64
}

////////////////////////////////////////////////////////////////////////////////
/// Workflow Phase
////////////////////////////////////////////////////////////////////////////////

// Phase represents the phase of a workflow.
type Phase string

const (
	PhasePending   Phase = "Pending"
	PhaseRunning   Phase = "Running"
	PhaseSucceeded Phase = "Succeeded"
	PhaseFailed    Phase = "Failed"
	PhaseError     Phase = "Error"
)

// IsTerminal returns true if the phase is terminal.
func (p Phase) IsTerminal() bool {
	return p == PhaseSucceeded || p == PhaseFailed || p == PhaseError
}

////////////////////////////////////////////////////////////////////////////////
/// Defaults
////////////////////////////////////////////////////////////////////////////////

const (
	defaultServerURL = "https://localhost:2746"
	defaultNamespace = "default"
	apiVersion       = "argoproj.io/v1alpha1"
	apiPathPrefix    = "/api/v1/workflows"
)

////////////////////////////////////////////////////////////////////////////////
/// Workflow Types (simplified for REST API interaction)
////////////////////////////////////////////////////////////////////////////////

// Workflow represents an Argo Workflow resource.
type Workflow struct {
	APIVersion string          `json:"apiVersion,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Metadata   ObjectMeta      `json:"metadata,omitempty"`
	Spec       WorkflowSpec    `json:"spec,omitempty"`
	Status     *WorkflowStatus `json:"status,omitempty"`
}

// ObjectMeta represents object metadata.
type ObjectMeta struct {
	Name              string            `json:"name,omitempty"`
	GenerateName      string            `json:"generateName,omitempty"`
	Namespace         string            `json:"namespace,omitempty"`
	UID               string            `json:"uid,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	CreationTimestamp time.Time         `json:"creationTimestamp,omitempty"`
}

// WorkflowSpec represents the spec of a workflow.
type WorkflowSpec struct {
	Entrypoint         string     `json:"entrypoint,omitempty"`
	Templates          []Template `json:"templates,omitempty"`
	Arguments          Arguments  `json:"arguments,omitempty"`
	ServiceAccountName string     `json:"serviceAccountName,omitempty"`
}

// Template represents a workflow template.
type Template struct {
	Name      string       `json:"name,omitempty"`
	Container *Container   `json:"container,omitempty"`
	Script    *Script      `json:"script,omitempty"`
	DAG       *DAGTemplate `json:"dag,omitempty"`
	Steps     []Step       `json:"steps,omitempty"`
}

// Container represents a container template.
type Container struct {
	Image   string   `json:"image,omitempty"`
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// Script represents a script template.
type Script struct {
	Image   string   `json:"image,omitempty"`
	Command []string `json:"command,omitempty"`
	Source  string   `json:"source,omitempty"`
}

// DAGTemplate represents a DAG template.
type DAGTemplate struct {
	Tasks []DAGTask `json:"tasks,omitempty"`
}

// DAGTask represents a task in a DAG template.
type DAGTask struct {
	Name         string    `json:"name,omitempty"`
	Template     string    `json:"template,omitempty"`
	Arguments    Arguments `json:"arguments,omitempty"`
	Dependencies []string  `json:"dependencies,omitempty"`
}

// Step represents a step in a steps template.
type Step struct {
	Name      string    `json:"name,omitempty"`
	Template  string    `json:"template,omitempty"`
	Arguments Arguments `json:"arguments,omitempty"`
}

// Arguments represents workflow or step arguments.
type Arguments struct {
	Parameters []Parameter `json:"parameters,omitempty"`
}

// Parameter represents a workflow parameter.
type Parameter struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// WorkflowStatus represents the status of a workflow.
type WorkflowStatus struct {
	Phase      Phase                 `json:"phase,omitempty"`
	StartedAt  *time.Time            `json:"startedAt,omitempty"`
	FinishedAt *time.Time            `json:"finishedAt,omitempty"`
	Message    string                `json:"message,omitempty"`
	Nodes      map[string]NodeStatus `json:"nodes,omitempty"`
}

// NodeStatus represents the status of a node in the workflow.
type NodeStatus struct {
	ID           string     `json:"id,omitempty"`
	Name         string     `json:"name,omitempty"`
	TemplateName string     `json:"templateName,omitempty"`
	Phase        Phase      `json:"phase,omitempty"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	Message      string     `json:"message,omitempty"`
}

// WorkflowList is a list of workflows.
type WorkflowList struct {
	Items []Workflow `json:"items,omitempty"`
}
