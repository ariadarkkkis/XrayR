package release_test

import (
	"bufio"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	expectedApplicationVersion = "0.9.6-26.3.27"
	expectedXrayRelease        = "v26.3.27"
	expectedXrayModule         = "v1.260327.0"
)

func TestDefaultTestsDoNotDependOnLiveServices(t *testing.T) {
	repositoryRoot := repositoryRoot(t)
	var violations []string

	require.NoError(t, filepath.WalkDir(repositoryRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "out" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}

		active, err := build.Default.MatchFile(filepath.Dir(path), entry.Name())
		if err != nil {
			return err
		}
		if !active {
			return nil
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.BasicLit:
				literal := strings.Trim(value.Value, "`\"")
				parsedURL, err := url.Parse(literal)
				if err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && parsedURL.Host != "" {
					violations = append(violations, relativePath(repositoryRoot, path)+": live URL "+literal)
				}
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "DNSCert" || selector.Sel.Name == "HTTPCert" || selector.Sel.Name == "RenewCert") {
					violations = append(violations, relativePath(repositoryRoot, path)+": ACME call "+selector.Sel.Name)
				}
			}
			return true
		})
		return nil
	}))

	require.Empty(t, violations, "default tests must be hermetic; move live tests behind //go:build integration")
}

func TestProductionContainerUsesAlignedBuilderAndPackagedAssets(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join(repositoryRoot(t), "Dockerfile"))
	require.NoError(t, err)
	contents := string(dockerfile)

	require.Contains(t, contents, "FROM golang:1.26.0-alpine3.23@sha256:d4c4845f5d60c6a974c6000ce58ae079328d03ab7f721a0734277e69905473e5 AS builder")
	require.Contains(t, contents, "FROM scratch")
	require.NotContains(t, contents, "raw.githubusercontent.com", "container builds must use the repository's qualified assets")
	require.Contains(t, contents, "COPY release/config/geoip.dat /etc/XrayR/geoip.dat")
	require.Contains(t, contents, "COPY release/config/geosite.dat /etc/XrayR/geosite.dat")
}

func TestReleaseBuildRejectsUnrecordedSourceChanges(t *testing.T) {
	buildScript, err := os.ReadFile(filepath.Join(repositoryRoot(t), "scripts", "build-release.sh"))
	require.NoError(t, err)
	contents := string(buildScript)

	require.Contains(t, contents, "git diff --quiet HEAD --")
	require.Contains(t, contents, "git -C \"$repository_root\" status --porcelain --untracked-files=all")
	require.Contains(t, contents, "release inputs differ from the recorded source commit")
}

func TestReleaseStartupSmokeRequiresLongRunningService(t *testing.T) {
	smokeScript, err := os.ReadFile(filepath.Join(repositoryRoot(t), "scripts", "smoke-release.sh"))
	require.NoError(t, err)
	contents := string(smokeScript)

	require.Contains(t, contents, `[ "$startup_status" -eq 124 ]`)
	require.NotContains(t, contents, `[ "$startup_status" -eq 0 ] ||`)
}

func TestReleaseMetadataMatchesEmbeddedVersions(t *testing.T) {
	root := repositoryRoot(t)
	metadata := readEnvironmentFile(t, filepath.Join(root, "release", "release.env"))
	require.Equal(t, expectedApplicationVersion, metadata["XRAYR_VERSION"])
	require.Equal(t, expectedXrayRelease, metadata["XRAY_CORE_RELEASE"])
	require.Equal(t, expectedXrayModule, metadata["XRAY_CORE_MODULE_VERSION"])

	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	require.Contains(t, string(goMod), "github.com/xtls/xray-core "+expectedXrayModule)

	versionSource, err := os.ReadFile(filepath.Join(root, "cmd", "version.go"))
	require.NoError(t, err)
	require.Contains(t, string(versionSource), `version  = "`+expectedApplicationVersion+`"`)
}

func readEnvironmentFile(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "invalid metadata line %q", line)
		values[key] = value
	}
	require.NoError(t, scanner.Err())
	return values
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(source), ".."))
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}
