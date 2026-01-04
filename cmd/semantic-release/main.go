package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"github.com/Masterminds/semver/v3"
	"github.com/go-semantic-release/semantic-release/v2/pkg/config"
	"github.com/go-semantic-release/semantic-release/v2/pkg/generator"
	"github.com/go-semantic-release/semantic-release/v2/pkg/hooks"
	"github.com/go-semantic-release/semantic-release/v2/pkg/plugin/manager"
	"github.com/go-semantic-release/semantic-release/v2/pkg/provider"
	"github.com/go-semantic-release/semantic-release/v2/pkg/semrel"
	"github.com/spf13/cobra"
)

// SRVERSION is the semantic-release version (added at compile time)
var SRVERSION string

var logger = log.New(os.Stderr, "[go-semantic-release]: ", 0)

var exitHandler func()

func exitIfError(err error, exitCode ...int) {
	if err == nil {
		return
	}
	logger.Println(err)
	if exitHandler != nil {
		exitHandler()
	}
	if len(exitCode) == 1 {
		os.Exit(exitCode[0])
		return
	}
	os.Exit(1)
}

func main() {
	cmd := &cobra.Command{
		Use:     "semantic-release",
		Short:   "semantic-release - fully automated package/module/image publishing",
		Run:     cliHandler,
		Version: SRVERSION,
	}

	config.SetFlags(cmd)
	cobra.OnInitialize(func() {
		err := config.InitConfig(cmd)
		if err != nil {
			logger.Printf("Config error: %s", err.Error())
			os.Exit(1)
			return
		}
	})
	err := cmd.Execute()
	if err != nil {
		logger.Printf("ERROR: %s", err.Error())
		os.Exit(1)
	}
}

func mergeConfigWithDefaults(defaults, conf map[string]string) {
	for k, v := range conf {
		defaults[k] = v
		// case-insensitive overwrite default values
		keyLower := strings.ToLower(k)
		for dk := range defaults {
			if strings.ToLower(dk) == keyLower && dk != k {
				defaults[dk] = v
			}
		}
	}
}

func updateFilesBeforeRelease(patterns []string, version string) error {
	v, err := semver.NewVersion(version)
	if err != nil {
		return fmt.Errorf("invalid version: %w", err)
	}

	replacements := map[string]string{
		"{{version}}": version,
		"{{major}}":   fmt.Sprintf("%d", v.Major()),
		"{{minor}}":   fmt.Sprintf("%d", v.Minor()),
		"{{patch}}":   fmt.Sprintf("%d", v.Patch()),
	}

	for _, pattern := range patterns {
		parts := strings.SplitN(pattern, ":", 3)
		if len(parts) != 3 {
			return fmt.Errorf("invalid pattern format: %s (expected file:regex:template)", pattern)
		}

		file := strings.TrimSpace(parts[0])
		regexStr := strings.TrimSpace(parts[1])
		template := parts[2]
		if file == "" || regexStr == "" {
			return fmt.Errorf("invalid pattern format: %s (expected file:regex:template)", pattern)
		}

		for k, v := range replacements {
			template = strings.ReplaceAll(template, k, v)
		}

		fi, err := os.Stat(file)
		if err != nil {
			return fmt.Errorf("failed to stat %s: %w", file, err)
		}

		content, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", file, err)
		}

		re, err := regexp.Compile(regexStr)
		if err != nil {
			return fmt.Errorf("invalid regex in pattern %s: %w", pattern, err)
		}
		if !re.Match(content) {
			return fmt.Errorf("pattern did not match any content in %s (regex=%q)", file, regexStr)
		}

		oldContent := string(content)
		newContent := re.ReplaceAllString(oldContent, template)
		if newContent == oldContent {
			logger.Printf("no changes for %s\n", file)
			continue
		}

		if err := os.WriteFile(file, []byte(newContent), fi.Mode()); err != nil {
			return fmt.Errorf("failed to write %s: %w", file, err)
		}

		logger.Printf("updated %s\n", file)
	}

	return nil
}

func commitAndPushChanges(files []string, message string, branch string) (string, error) {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	branch = strings.TrimPrefix(branch, "origin/")
	if branch == "" {
		return "", fmt.Errorf("branch is empty")
	}

	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("index has staged changes; refusing to commit update-before changes")
		}
		return "", fmt.Errorf("failed to check index state: %w", err)
	}

	for _, file := range files {
		if strings.TrimSpace(file) == "" {
			continue
		}
		cmd := exec.Command("git", "add", "--", file)
		if output, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git add failed: %s: %w", output, err)
		}
	}

	cmd = exec.Command("git", "diff", "--cached", "--quiet")
	if err := cmd.Run(); err == nil {
		cmd = exec.Command("git", "rev-parse", "HEAD")
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to get SHA: %w", err)
		}
		return strings.TrimSpace(string(output)), nil
	} else if _, ok := err.(*exec.ExitError); !ok {
		return "", fmt.Errorf("failed to check staged diff: %w", err)
	}

	cmd = exec.Command("git", "commit", "-m", message)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit failed: %s: %w", output, err)
	}

	cmd = exec.Command("git", "push", "origin", "HEAD:"+branch)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git push failed: %s: %w", output, err)
	}

	cmd = exec.Command("git", "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get new SHA: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

//gocyclo:ignore
func cliHandler(cmd *cobra.Command, _ []string) {
	logger.Printf("version: %s\n", SRVERSION)

	conf, err := config.NewConfig(cmd)
	exitIfError(err)

	pluginManager, err := manager.New(conf)
	exitIfError(err)
	exitHandler = func() {
		logger.Println("stopping plugins...")
		pluginManager.Stop()
	}
	defer exitHandler()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		termSignal := <-c
		logger.Println("terminating...")
		exitIfError(fmt.Errorf("received signal: %s", termSignal))
	}()

	if conf.DownloadPlugins {
		exitIfError(pluginManager.FetchAllPlugins())
		logger.Println("all plugins were downloaded!")
		return
	}

	if !conf.PluginResolverDisableBatchPrefetch {
		logger.Println("trying to prefetch plugins...")
		pluginsWerePrefetched, _, pErr := pluginManager.PrefetchAllPluginsIfBatchIsPossible()
		if pErr != nil {
			logger.Printf("warning: failed to prefetch plugins: %v", pErr)
		} else {
			if pluginsWerePrefetched {
				logger.Println("all plugins were prefetched!")
			} else {
				logger.Println("prefetching plugins was not possible.")
			}
		}
	}

	ci, err := pluginManager.GetCICondition()
	exitIfError(err)
	ciName := ci.Name()
	logger.Printf("ci-condition plugin: %s@%s\n", ciName, ci.Version())

	prov, err := pluginManager.GetProvider()
	exitIfError(err)
	provName := prov.Name()
	logger.Printf("provider plugin: %s@%s\n", provName, prov.Version())

	if conf.ProviderOpts["token"] == "" {
		conf.ProviderOpts["token"] = conf.Token
	}
	err = prov.Init(conf.ProviderOpts)
	exitIfError(err)

	logger.Println("getting default branch...")
	repoInfo, err := prov.GetInfo()
	exitIfError(err)
	logger.Println("found default branch: " + repoInfo.DefaultBranch)
	if repoInfo.Private {
		logger.Println("repo is private")
	}

	currentBranch := ci.GetCurrentBranch()
	if currentBranch == "" {
		exitIfError(fmt.Errorf("current branch not found"))
	}
	logger.Println("found current branch: " + currentBranch)

	if !conf.AllowMaintainedVersionOnDefaultBranch && conf.MaintainedVersion != "" && currentBranch == repoInfo.DefaultBranch {
		exitIfError(fmt.Errorf("maintained version not allowed on default branch"))
	}

	if conf.MaintainedVersion != "" {
		logger.Println("found maintained version: " + conf.MaintainedVersion)
		repoInfo.DefaultBranch = "*"
	}

	currentSha := ci.GetCurrentSHA()
	logger.Println("found current sha: " + currentSha)

	hooksExecutor, err := pluginManager.GetChainedHooksExecutor()
	exitIfError(err)

	hooksNames := hooksExecutor.GetNameVersionPairs()
	if len(hooksNames) > 0 {
		logger.Printf("hooks plugins: %s\n", strings.Join(hooksNames, ", "))
	}

	hooksConfig := map[string]string{
		"provider":      provName,
		"ci":            ciName,
		"currentBranch": currentBranch,
		"currentSha":    currentSha,
		"defaultBranch": repoInfo.DefaultBranch,
		"prerelease":    fmt.Sprintf("%t", conf.Prerelease),
	}
	mergeConfigWithDefaults(hooksConfig, conf.HooksOpts)

	exitIfError(hooksExecutor.Init(hooksConfig))

	if !conf.NoCi {
		logger.Println("running CI condition...")
		conditionConfig := map[string]string{
			"token":         conf.Token,
			"defaultBranch": repoInfo.DefaultBranch,
			"private":       fmt.Sprintf("%t", repoInfo.Private),
		}
		mergeConfigWithDefaults(conditionConfig, conf.CiConditionOpts)

		err = ci.RunCondition(conditionConfig)
		if err != nil {
			herr := hooksExecutor.NoRelease(&hooks.NoReleaseConfig{
				Reason:  hooks.NoReleaseReason_CONDITION,
				Message: err.Error(),
			})
			if herr != nil {
				logger.Printf("there was an error executing the hooks plugins: %s", herr.Error())
			}
			exitIfError(err, 66)
		}

	}

	logger.Println("getting latest release...")
	matchRegex := ""
	match := strings.TrimSpace(conf.Match)
	if match != "" {
		logger.Printf("getting latest release matching %s...", match)
		matchRegex = "^" + match
	}
	releases, err := prov.GetReleases(matchRegex)
	exitIfError(err)
	release, err := semrel.GetLatestReleaseFromReleases(releases, conf.MaintainedVersion)
	exitIfError(err)
	logger.Println("found version: " + release.Version)

	if strings.Contains(conf.MaintainedVersion, "-") && semver.MustParse(release.Version).Prerelease() == "" {
		exitIfError(fmt.Errorf("no pre-release for this version possible"))
	}

	logger.Println("getting commits...")
	rawCommits, err := prov.GetCommits(release.SHA, currentSha)
	exitIfError(err)

	logger.Println("analyzing commits...")
	commitAnalyzer, err := pluginManager.GetCommitAnalyzer()
	exitIfError(err)
	logger.Printf("commit-analyzer plugin: %s@%s\n", commitAnalyzer.Name(), commitAnalyzer.Version())
	exitIfError(commitAnalyzer.Init(conf.CommitAnalyzerOpts))

	commits := commitAnalyzer.Analyze(rawCommits)

	logger.Println("calculating new version...")
	newVer := semrel.GetNewVersion(conf, commits, release)
	if newVer == "" {
		herr := hooksExecutor.NoRelease(&hooks.NoReleaseConfig{
			Reason:  hooks.NoReleaseReason_NO_CHANGE,
			Message: "",
		})
		if herr != nil {
			logger.Printf("there was an error executing the hooks plugins: %s", herr.Error())
		}
		errNoChange := errors.New("no change")
		if conf.AllowNoChanges {
			exitIfError(errNoChange, 0)
		} else {
			exitIfError(errNoChange, 65)
		}
	}
	logger.Println("new version: " + newVer)

	logger.Println("generating changelog...")
	changelogGenerator, err := pluginManager.GetChangelogGenerator()
	exitIfError(err)
	logger.Printf("changelog-generator plugin: %s@%s\n", changelogGenerator.Name(), changelogGenerator.Version())
	exitIfError(changelogGenerator.Init(conf.ChangelogGeneratorOpts))

	changelogRes := changelogGenerator.Generate(&generator.ChangelogGeneratorConfig{
		Commits:       commits,
		LatestRelease: release,
		NewVersion:    newVer,
	})
	if conf.Changelog != "" {
		oldFile := make([]byte, 0)
		if conf.PrependChangelog {
			oldFileData, err := os.ReadFile(conf.Changelog)
			if err == nil {
				oldFile = append([]byte("\n"), oldFileData...)
			}
		}
		changelogData := append([]byte(changelogRes), oldFile...)
		exitIfError(os.WriteFile(conf.Changelog, changelogData, 0o644))
	}

	if conf.Dry {
		if conf.VersionFile {
			exitIfError(os.WriteFile(".version-unreleased", []byte(newVer), 0o644))
		}
		exitIfError(errors.New("DRY RUN: no release was created"), 0)
	}

	// update files before release if specified
	if len(conf.UpdateFilesBefore) > 0 {
		logger.Println("updating files before release...")
		if err := updateFilesBeforeRelease(conf.UpdateFilesBefore, newVer); err != nil {
			exitIfError(err)
		}

		filesToCommit := make([]string, 0, len(conf.UpdateFilesBefore))
		for _, pattern := range conf.UpdateFilesBefore {
			parts := strings.SplitN(pattern, ":", 3)
			if len(parts) == 3 {
				filesToCommit = append(filesToCommit, strings.TrimSpace(parts[0]))
			}
		}

		commitMsg := conf.FilesUpdaterOpts["commit-message"]
		if commitMsg == "" {
			commitMsg = fmt.Sprintf("chore: bump version to %s", newVer)
		}
		commitMsg = strings.ReplaceAll(commitMsg, "{{version}}", newVer)

		logger.Println("committing and pushing changes...")
		newSHA, err := commitAndPushChanges(filesToCommit, commitMsg, currentBranch)
		if err != nil {
			exitIfError(err)
		}

		currentSha = newSHA
		logger.Printf("updated SHA to %s\n", currentSha)
	}

	draft := false
	// only accept exact "true" string per spec - other values default to false
	if draftOpt, ok := conf.ProviderOpts["draft"]; ok {
		if draftOpt == "true" {
			draft = true
		} else {
			logger.Printf("warning: draft option value '%s' is not 'true', defaulting to published release\n", draftOpt)
		}
	}

	if draft {
		logger.Println("creating draft release...")
	} else {
		logger.Println("creating published release...")
	}

	newRelease := &provider.CreateReleaseConfig{
		Changelog:  changelogRes,
		NewVersion: newVer,
		Prerelease: conf.Prerelease,
		Branch:     currentBranch,
		SHA:        currentSha,
		Draft:      draft,
	}
	exitIfError(prov.CreateRelease(newRelease))

	if conf.Ghr {
		exitIfError(os.WriteFile(".ghr", []byte(fmt.Sprintf("-u %s -r %s v%s", repoInfo.Owner, repoInfo.Repo, newVer)), 0o644))
	}

	if conf.VersionFile {
		exitIfError(os.WriteFile(".version", []byte(newVer), 0o644))
	}

	if len(conf.UpdateFiles) == 0 && len(conf.FilesUpdaterPlugins) > 0 {
		logger.Println("warning: file update plugins found but no files marked for update. You may be missing the update flag, e.g. --update package.json")
	}

	if len(conf.UpdateFiles) > 0 {
		logger.Println("updating files...")
		updater, err := pluginManager.GetChainedUpdater()
		exitIfError(err)
		logger.Printf("files-updater plugins: %s\n", strings.Join(updater.GetNameVersionPairs(), ", "))
		exitIfError(updater.Init(conf.FilesUpdaterOpts))

		for _, f := range conf.UpdateFiles {
			exitIfError(updater.Apply(f, newVer))
		}
	}

	herr := hooksExecutor.Success(&hooks.SuccessHookConfig{
		Commits:     commits,
		PrevRelease: release,
		NewRelease: &semrel.Release{
			SHA:     currentSha,
			Version: newVer,
		},
		Changelog: changelogRes,
		RepoInfo:  repoInfo,
	})
	exitIfError(herr)

	logger.Println("done.")
}
