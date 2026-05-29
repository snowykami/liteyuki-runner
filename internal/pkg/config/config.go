// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
	"go.yaml.in/yaml/v4"
)

// Log represents the configuration for logging.
type Log struct {
	Level string `yaml:"level"` // Level indicates the logging level.
}

// Runner represents the configuration for the runner.
type Runner struct {
	File                string            `yaml:"file"`                   // File specifies the file path for the runner.
	Capacity            int               `yaml:"capacity"`               // Capacity specifies the capacity of the runner.
	Envs                map[string]string `yaml:"envs"`                   // Envs stores environment variables for the runner.
	EnvFile             string            `yaml:"env_file"`               // EnvFile specifies the path to the file containing environment variables for the runner.
	Timeout             time.Duration     `yaml:"timeout"`                // Timeout specifies the duration for runner timeout.
	ShutdownTimeout     time.Duration     `yaml:"shutdown_timeout"`       // ShutdownTimeout specifies the duration to wait for running jobs to complete during a shutdown of the runner.
	Insecure            bool              `yaml:"insecure"`               // Insecure indicates whether the runner operates in an insecure mode.
	FetchTimeout        time.Duration     `yaml:"fetch_timeout"`          // FetchTimeout specifies the timeout duration for fetching resources.
	FetchInterval       time.Duration     `yaml:"fetch_interval"`         // FetchInterval specifies the interval duration for fetching resources.
	FetchIntervalMax    time.Duration     `yaml:"fetch_interval_max"`     // FetchIntervalMax specifies the maximum backoff interval when idle.
	WorkdirCleanupAge   time.Duration     `yaml:"workdir_cleanup_age"`    // WorkdirCleanupAge removes stale bind-workdir task directories older than this duration during idle cleanup.
	IdleCleanupInterval time.Duration     `yaml:"idle_cleanup_interval"`  // IdleCleanupInterval runs stale bind-workdir cleanup periodically while the runner is idle. Set to 0 to disable cleanup cadence.
	LogReportInterval   time.Duration     `yaml:"log_report_interval"`    // LogReportInterval specifies the base interval for periodic log flush.
	LogReportMaxLatency time.Duration     `yaml:"log_report_max_latency"` // LogReportMaxLatency specifies the max time a log row can wait before being sent.
	LogReportBatchSize  int               `yaml:"log_report_batch_size"`  // LogReportBatchSize triggers immediate log flush when buffer reaches this size.
	StateReportInterval time.Duration     `yaml:"state_report_interval"`  // StateReportInterval specifies the interval for state reporting.
	ReportCloseTimeout  time.Duration     `yaml:"report_close_timeout"`   // ReportCloseTimeout caps each RPC attempt when flushing the final logs and task state at job completion, on a detached context so a server cancel can't block the acknowledgement.
	Labels              []string          `yaml:"labels"`                 // Labels specify the labels of the runner. Labels are declared on each startup
	GithubMirror        string            `yaml:"github_mirror"`          // GithubMirror defines what mirrors should be used when using github
	AllocatePTY         bool              `yaml:"allocate_pty"`           // AllocatePTY allocates a pseudo-TTY for each step's process. Default is false, matching GitHub's actions/runner. Enable only for jobs that need an interactive terminal; tools like docker build emit redrawing progress frames into the captured log when a TTY is present. Applies to both host and docker backends.
	AllowedRepos        []string          `yaml:"allowed_repos"`          // AllowedRepos specifies which repositories this runner may run jobs for. Supports owner/repo, owner/*, */repo, and */*.
	BlacklistMode       bool              `yaml:"blacklist_mode"`         // BlacklistMode treats AllowedRepos as a deny list instead of an allow list.
	RejectText          string            `yaml:"reject_text"`            // RejectText is logged when a job is rejected. {REPO} and {RUNNER} are replaced.
}

// Cache represents the configuration for caching.
type Cache struct {
	Enabled        *bool  `yaml:"enabled"`         // Enabled indicates whether caching is enabled. It is a pointer to distinguish between false and not set. If not set, it will be true.
	Dir            string `yaml:"dir"`             // Dir specifies the directory path for caching.
	Host           string `yaml:"host"`            // Host specifies the caching host.
	Port           uint16 `yaml:"port"`            // Port specifies the caching port.
	ExternalServer string `yaml:"external_server"` // ExternalServer specifies the URL of external cache server
	ExternalSecret string `yaml:"external_secret"` // ExternalSecret is a shared secret between this runner and an external gitea-runner cache-server, enabling per-job ACTIONS_RUNTIME_TOKEN authentication and repo scoping over the network. Leave empty to keep the legacy unauthenticated behavior.
	OfflineMode    bool   `yaml:"offline_mode"`    // OfflineMode reuses a cached action without fetching from the remote; a moved tag or branch stays at the cached commit until the cache entry is removed.
}

// Container represents the configuration for the container.
type Container struct {
	Network       string        `yaml:"network"`        // Network specifies the network for the container.
	NetworkMode   string        `yaml:"network_mode"`   // Deprecated: use Network instead. Could be removed after Gitea 1.20
	Privileged    bool          `yaml:"privileged"`     // Privileged indicates whether the container runs in privileged mode.
	Options       string        `yaml:"options"`        // Options specifies additional options for the container.
	WorkdirParent string        `yaml:"workdir_parent"` // WorkdirParent specifies the parent directory for the container's working directory.
	ValidVolumes  []string      `yaml:"valid_volumes"`  // ValidVolumes specifies the volumes (including bind mounts) can be mounted to containers.
	DockerHost    string        `yaml:"docker_host"`    // DockerHost specifies the Docker host. It overrides the value specified in environment variable DOCKER_HOST.
	ForcePull     bool          `yaml:"force_pull"`     // Pull docker image(s) even if already present
	ForceRebuild  bool          `yaml:"force_rebuild"`  // Rebuild docker image(s) even if already present
	RequireDocker bool          `yaml:"require_docker"` // Always require a reachable docker daemon, even if not required by runner
	DockerTimeout time.Duration `yaml:"docker_timeout"` // Timeout to wait for the docker daemon to be reachable, if docker is required by require_docker or runner
	BindWorkdir   bool          `yaml:"bind_workdir"`   // BindWorkdir binds the workspace to the host filesystem instead of using Docker volumes. Required for DinD when jobs use docker compose with bind mounts.
}

// Host represents the configuration for the host.
type Host struct {
	WorkdirParent string `yaml:"workdir_parent"` // WorkdirParent specifies the parent directory for the host's working directory.
}

// Metrics represents the configuration for the Prometheus metrics endpoint.
type Metrics struct {
	Enabled bool   `yaml:"enabled"` // Enabled indicates whether the metrics endpoint is exposed.
	Addr    string `yaml:"addr"`    // Addr specifies the listen address for the metrics HTTP server (e.g., ":9101").
}

// Config represents the overall configuration.
type Config struct {
	Log       Log       `yaml:"log"`       // Log represents the configuration for logging.
	Runner    Runner    `yaml:"runner"`    // Runner represents the configuration for the runner.
	Cache     Cache     `yaml:"cache"`     // Cache represents the configuration for caching.
	Container Container `yaml:"container"` // Container represents the configuration for the container.
	Host      Host      `yaml:"host"`      // Host represents the configuration for the host.
	Metrics   Metrics   `yaml:"metrics"`   // Metrics represents the configuration for the Prometheus metrics endpoint.
}

// LoadDefault returns the default configuration.
// If file is not empty, it will be used to load the configuration.
func LoadDefault(file string) (*Config, error) {
	cfg := &Config{}
	definedRunnerKeys := map[string]bool{}
	if file != "" {
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("open config file %q: %w", file, err)
		}
		if err := yaml.Unmarshal(content, cfg); err != nil {
			return nil, fmt.Errorf("parse config file %q: %w", file, err)
		}
		definedRunnerKeys, err = definedRunnerConfigKeys(content)
		if err != nil {
			return nil, fmt.Errorf("parse config file %q for defaults metadata: %w", file, err)
		}
	}

	if cfg.Runner.EnvFile != "" {
		if stat, err := os.Stat(cfg.Runner.EnvFile); err == nil && !stat.IsDir() {
			envs, err := godotenv.Read(cfg.Runner.EnvFile)
			if err != nil {
				return nil, fmt.Errorf("read env file %q: %w", cfg.Runner.EnvFile, err)
			}
			if cfg.Runner.Envs == nil {
				cfg.Runner.Envs = map[string]string{}
			}
			maps.Copy(cfg.Runner.Envs, envs)
		}
	}

	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Runner.File == "" {
		cfg.Runner.File = ".runner"
	}
	if cfg.Runner.Capacity <= 0 {
		cfg.Runner.Capacity = 1
	}
	if cfg.Runner.Timeout <= 0 {
		cfg.Runner.Timeout = 3 * time.Hour
	}
	if cfg.Cache.Enabled == nil {
		b := true
		cfg.Cache.Enabled = &b
	}
	if *cfg.Cache.Enabled {
		if cfg.Cache.Dir == "" {
			home, _ := os.UserHomeDir()
			cfg.Cache.Dir = filepath.Join(home, ".cache", "actcache")
		}
		if cfg.Cache.ExternalServer != "" && cfg.Cache.ExternalSecret == "" {
			return nil, errors.New("cache.external_server is set but cache.external_secret is empty; configure the same external_secret on this runner and the gitea-runner cache-server")
		}
	}
	if cfg.Container.WorkdirParent == "" {
		cfg.Container.WorkdirParent = "workspace"
	}
	if cfg.Host.WorkdirParent == "" {
		home, _ := os.UserHomeDir()
		cfg.Host.WorkdirParent = filepath.Join(home, ".cache", "act")
	}
	if cfg.Runner.FetchTimeout <= 0 {
		cfg.Runner.FetchTimeout = 5 * time.Second
	}
	if cfg.Runner.FetchInterval <= 0 {
		cfg.Runner.FetchInterval = 2 * time.Second
	}
	if cfg.Runner.FetchIntervalMax <= 0 {
		cfg.Runner.FetchIntervalMax = 5 * time.Second
	}
	if cfg.Runner.WorkdirCleanupAge == 0 && !definedRunnerKeys["workdir_cleanup_age"] {
		cfg.Runner.WorkdirCleanupAge = 24 * time.Hour
	}
	if cfg.Runner.IdleCleanupInterval == 0 && !definedRunnerKeys["idle_cleanup_interval"] {
		cfg.Runner.IdleCleanupInterval = 10 * time.Minute
	}
	if cfg.Runner.LogReportInterval <= 0 {
		cfg.Runner.LogReportInterval = 5 * time.Second
	}
	if cfg.Runner.LogReportMaxLatency <= 0 {
		cfg.Runner.LogReportMaxLatency = 3 * time.Second
	}
	if cfg.Runner.LogReportBatchSize <= 0 {
		cfg.Runner.LogReportBatchSize = 100
	}
	if cfg.Runner.StateReportInterval <= 0 {
		cfg.Runner.StateReportInterval = 5 * time.Second
	}
	if cfg.Runner.ReportCloseTimeout <= 0 {
		cfg.Runner.ReportCloseTimeout = 10 * time.Second
	}
	if cfg.Runner.RejectText == "" {
		cfg.Runner.RejectText = "This runner is not allowed to run this job in this repository: {REPO}."
	}
	if cfg.Metrics.Addr == "" {
		cfg.Metrics.Addr = "127.0.0.1:9101"
	}

	// Validate and fix invalid config combinations to prevent confusing behavior.
	if cfg.Runner.FetchIntervalMax < cfg.Runner.FetchInterval {
		log.Warnf("fetch_interval_max (%v) is less than fetch_interval (%v), setting fetch_interval_max to fetch_interval",
			cfg.Runner.FetchIntervalMax, cfg.Runner.FetchInterval)
		cfg.Runner.FetchIntervalMax = cfg.Runner.FetchInterval
	}
	if cfg.Runner.LogReportMaxLatency >= cfg.Runner.LogReportInterval {
		log.Warnf("log_report_max_latency (%v) >= log_report_interval (%v), the max-latency timer will never fire before the periodic ticker; consider lowering log_report_max_latency",
			cfg.Runner.LogReportMaxLatency, cfg.Runner.LogReportInterval)
	}

	// although `container.network_mode` will be deprecated, but we have to be compatible with it for now.
	if cfg.Container.NetworkMode != "" && cfg.Container.Network == "" {
		log.Warn("You are trying to use deprecated configuration item of `container.network_mode`, please use `container.network` instead.")
		if cfg.Container.NetworkMode == "bridge" {
			// Previously, if the value of `container.network_mode` is `bridge`, we will create a new network for job.
			// But “bridge” is easily confused with the bridge network created by Docker by default.
			// So we set the value of `container.network` to empty string to make `runner` automatically create a new network for job.
			cfg.Container.Network = ""
		} else {
			cfg.Container.Network = cfg.Container.NetworkMode
		}
	}

	return cfg, nil
}

func definedRunnerConfigKeys(content []byte) (map[string]bool, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, err
	}

	defined := map[string]bool{}
	if len(root.Content) == 0 {
		return defined, nil
	}

	doc := root.Content[0]
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i]
		value := doc.Content[i+1]
		if key.Value != "runner" || value.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(value.Content); j += 2 {
			defined[value.Content[j].Value] = true
		}
		break
	}

	return defined, nil
}
