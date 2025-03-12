package application

// Status
// This structure is built on packaging, and saved in OCI image
// And used on deployment to build helmRelease object
// (Also used by render command)
type Status struct {
	ApiVersion string `json:"apiVersion"` // v1alpha1

	ChartByModule map[string]ChartRef `json:"chartByModule"`
}
