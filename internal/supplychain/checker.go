package supplychain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// Verdict represents the outcome of a package check.
type Verdict string

const (
	VerdictClean          Verdict = "clean"
	VerdictNotFound       Verdict = "not_found"
	VerdictSuspiciousAge  Verdict = "suspicious_age"
	VerdictLowDownloads   Verdict = "low_downloads"
	VerdictUnknown        Verdict = "unknown"
)

// PackageResult holds the result of checking a single package.
type PackageResult struct {
	Package    PackageRef `json:"package"`
	Verdict    Verdict    `json:"verdict"`
	Detail     string     `json:"detail,omitempty"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	Downloads  int64      `json:"downloads,omitempty"`
	CheckedAt  time.Time  `json:"checked_at"`
}

// CheckerConfig configures the PackageChecker behaviour.
type CheckerConfig struct {
	MinAgeDays     int  // Flag packages younger than this (default 7)
	TimeoutSec     int  // HTTP timeout per registry lookup (default 5)
	CheckDownloads bool // Whether to check download counts
}

// PackageChecker validates package names against registry APIs.
type PackageChecker struct {
	client         *http.Client
	minAgeDays     int
	checkDownloads bool
}

// NewPackageChecker creates a PackageChecker from the given configuration.
func NewPackageChecker(cfg CheckerConfig) *PackageChecker {
	if cfg.MinAgeDays <= 0 {
		cfg.MinAgeDays = 7
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 5
	}

	retryClient := retryablehttp.NewClient()
	retryClient.RetryMax = 1
	retryClient.Logger = nil // suppress default logger
	httpClient := retryClient.StandardClient()
	httpClient.Timeout = time.Duration(cfg.TimeoutSec) * time.Second

	return &PackageChecker{
		client:         httpClient,
		minAgeDays:     cfg.MinAgeDays,
		checkDownloads: cfg.CheckDownloads,
	}
}

// CheckPackage queries the appropriate registry for the given package and
// returns a verdict indicating whether the package looks safe.
func (pc *PackageChecker) CheckPackage(ctx context.Context, name string, registry Registry) (*PackageResult, error) {
	switch registry {
	case RegistryNPM:
		return pc.checkNPM(ctx, name)
	case RegistryPyPI:
		return pc.checkPyPI(ctx, name)
	default:
		return &PackageResult{
			Package:   PackageRef{Name: name, Registry: registry},
			Verdict:   VerdictUnknown,
			Detail:    fmt.Sprintf("unsupported registry: %s", registry),
			CheckedAt: time.Now(),
		}, nil
	}
}

// CheckPackages checks multiple packages and returns all results.
// It stops early if the context is cancelled.
func (pc *PackageChecker) CheckPackages(ctx context.Context, refs []PackageRef) []PackageResult {
	results := make([]PackageResult, 0, len(refs))
	for _, ref := range refs {
		if ctx.Err() != nil {
			break
		}
		result, err := pc.CheckPackage(ctx, ref.Name, ref.Registry)
		if err != nil {
			slog.Warn("supply chain check failed",
				"package", ref.Name,
				"registry", ref.Registry,
				"error", err,
			)
			results = append(results, PackageResult{
				Package:   ref,
				Verdict:   VerdictUnknown,
				Detail:    err.Error(),
				CheckedAt: time.Now(),
			})
			continue
		}
		results = append(results, *result)
	}
	return results
}

// npmRegistryResponse is the subset of the npm registry JSON we care about.
type npmRegistryResponse struct {
	Name string          `json:"name"`
	Time npmTimeResponse `json:"time"`
}

type npmTimeResponse struct {
	Created string `json:"created"`
}

func (pc *PackageChecker) checkNPM(ctx context.Context, name string) (*PackageResult, error) {
	url := fmt.Sprintf("https://registry.npmjs.org/%s", name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building npm request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := pc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("npm registry request: %w", err)
	}
	defer resp.Body.Close()

	result := &PackageResult{
		Package:   PackageRef{Name: name, Registry: RegistryNPM},
		CheckedAt: time.Now(),
	}

	if resp.StatusCode == http.StatusNotFound {
		result.Verdict = VerdictNotFound
		result.Detail = "package does not exist on npm"
		return result, nil
	}
	if resp.StatusCode != http.StatusOK {
		result.Verdict = VerdictUnknown
		result.Detail = fmt.Sprintf("npm returned HTTP %d", resp.StatusCode)
		return result, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
	if err != nil {
		return nil, fmt.Errorf("reading npm response: %w", err)
	}

	var npmResp npmRegistryResponse
	if err := json.Unmarshal(body, &npmResp); err != nil {
		result.Verdict = VerdictUnknown
		result.Detail = "malformed npm registry response"
		return result, nil
	}

	// Check creation date
	if npmResp.Time.Created != "" {
		createdAt, err := time.Parse(time.RFC3339, npmResp.Time.Created)
		if err == nil {
			result.CreatedAt = &createdAt
			age := time.Since(createdAt)
			if age < time.Duration(pc.minAgeDays)*24*time.Hour {
				result.Verdict = VerdictSuspiciousAge
				result.Detail = fmt.Sprintf("package created %d days ago (threshold: %d)", int(age.Hours()/24), pc.minAgeDays)
				return result, nil
			}
		}
	}

	result.Verdict = VerdictClean
	return result, nil
}

// pypiResponse is the subset of the PyPI JSON API we care about.
type pypiResponse struct {
	Info pypiInfo          `json:"info"`
	URLs []pypiReleaseFile `json:"urls"`
}

type pypiInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type pypiReleaseFile struct {
	UploadTimeISO string `json:"upload_time_iso_8601"`
}

func (pc *PackageChecker) checkPyPI(ctx context.Context, name string) (*PackageResult, error) {
	url := fmt.Sprintf("https://pypi.org/pypi/%s/json", name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building pypi request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := pc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pypi registry request: %w", err)
	}
	defer resp.Body.Close()

	result := &PackageResult{
		Package:   PackageRef{Name: name, Registry: RegistryPyPI},
		CheckedAt: time.Now(),
	}

	if resp.StatusCode == http.StatusNotFound {
		result.Verdict = VerdictNotFound
		result.Detail = "package does not exist on PyPI"
		return result, nil
	}
	if resp.StatusCode != http.StatusOK {
		result.Verdict = VerdictUnknown
		result.Detail = fmt.Sprintf("pypi returned HTTP %d", resp.StatusCode)
		return result, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
	if err != nil {
		return nil, fmt.Errorf("reading pypi response: %w", err)
	}

	var pypiResp pypiResponse
	if err := json.Unmarshal(body, &pypiResp); err != nil {
		result.Verdict = VerdictUnknown
		result.Detail = "malformed pypi response"
		return result, nil
	}

	// Check upload time of the latest release files
	if len(pypiResp.URLs) > 0 && pypiResp.URLs[0].UploadTimeISO != "" {
		uploadTime, err := time.Parse(time.RFC3339, pypiResp.URLs[0].UploadTimeISO)
		if err == nil {
			result.CreatedAt = &uploadTime
			age := time.Since(uploadTime)
			if age < time.Duration(pc.minAgeDays)*24*time.Hour {
				result.Verdict = VerdictSuspiciousAge
				result.Detail = fmt.Sprintf("latest release uploaded %d days ago (threshold: %d)", int(age.Hours()/24), pc.minAgeDays)
				return result, nil
			}
		}
	}

	result.Verdict = VerdictClean
	return result, nil
}
