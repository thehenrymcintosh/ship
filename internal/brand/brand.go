// Package brand holds every name-bearing string, so renaming the tool is a
// one-line change here plus docs.
package brand

const (
	// Name is the binary and product name.
	Name = "ship"
	// Dir is the per-repo and per-user directory name (".ship").
	Dir = "." + Name
	// EnvPrefix prefixes every environment variable ship reads or exports.
	EnvPrefix = "SHIP_"
	// HomeEnv overrides the user-level directory (default ~/.ship).
	HomeEnv = EnvPrefix + "HOME"
	// CookieName holds the UI auth token.
	CookieName = Name + "_token"
	// BranchPrefix is used for default run branches ("ship/<run-id>").
	BranchPrefix = Name + "/"
	// WorktreeSuffix is appended to the repo name for the git worktree dir.
	WorktreeSuffix = "." + Name
	// LeaseHolderPrefix tags treehouse leases ("ship:<run-id>").
	LeaseHolderPrefix = Name + ":"
	// SchemaID is the $id of the generated pipeline schema.
	SchemaID = "https://" + Name + ".local/schema/pipeline.json"
	// ConfigSchemaID is the $id of the generated config schema.
	ConfigSchemaID = "https://" + Name + ".local/schema/config.json"
	// SkillName is the handoff skill directory name.
	SkillName = Name + "-handoff"
	// Repo is the GitHub repo releases are published to (owner/name).
	Repo = "thehenrymcintosh/" + Name
	// DesignSkillName is the user-invoked pipeline design skill.
	DesignSkillName = Name + "-design"
)

// Version is set at build time with -ldflags.
var Version = "0.1.0-dev"
