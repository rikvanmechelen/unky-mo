package project

// Project represents a known project/repo in the workspace.
type Project struct {
	Name        string   `toml:"name"`
	Path        string   `toml:"path"`
	Description string   `toml:"description,omitempty"`
	Language    string   `toml:"language,omitempty"`
	Tags        []string `toml:"tags,omitempty"`
}
