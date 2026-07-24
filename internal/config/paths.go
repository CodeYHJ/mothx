package config

import "path/filepath"

const ProjectDirName = ".mothx"

// ProjectPath returns a project-level path under .mothx in the current working directory.
func ProjectPath(elem ...string) string {
	return ProjectPathFor(".", elem...)
}

// ProjectPathFor returns a project-level path under cwd/.mothx.
func ProjectPathFor(cwd string, elem ...string) string {
	if cwd == "" {
		cwd = "."
	}
	parts := append([]string{cwd, ProjectDirName}, elem...)
	return filepath.Join(parts...)
}
