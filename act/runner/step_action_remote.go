// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/common/git"
	"gitea.com/gitea/runner/act/model"

	gogit "github.com/go-git/go-git/v5"
)

type stepActionRemote struct {
	Step                *model.Step
	RunContext          *RunContext
	compositeRunContext *RunContext
	compositeSteps      *compositeSteps
	readAction          readAction
	runAction           runAction
	action              *model.Action
	env                 map[string]string
	remoteAction        *remoteAction
	cacheDir            string
	resolvedSha         string
}

var stepActionRemoteNewCloneExecutor = git.NewGitCloneExecutor

func (sar *stepActionRemote) prepareActionExecutor() common.Executor {
	return func(ctx context.Context) error {
		if sar.remoteAction != nil && sar.action != nil {
			// we are already good to run
			return nil
		}

		// For gitea:
		// Since actions can specify the download source via a url prefix.
		// The prefix may contain some sensitive information that needs to be stored in secrets,
		// so we need to interpolate the expression value for uses first.
		sar.Step.Uses = sar.RunContext.NewExpressionEvaluator(ctx).Interpolate(ctx, sar.Step.Uses)

		sar.remoteAction = newRemoteAction(sar.Step.Uses)
		if sar.remoteAction == nil {
			return fmt.Errorf("Expected format {org}/{repo}[/path]@ref. Actual '%s' Input string was not in a correct format", sar.Step.Uses)
		}

		github := sar.getGithubContext(ctx)
		if sar.remoteAction.IsCheckout() && isLocalCheckout(github, sar.Step) && !sar.RunContext.Config.NoSkipCheckout {
			common.Logger(ctx).Debugf("Skipping local actions/checkout because workdir was already copied")
			return nil
		}

		for _, action := range sar.RunContext.Config.ReplaceGheActionWithGithubCom {
			if strings.EqualFold(fmt.Sprintf("%s/%s", sar.remoteAction.Org, sar.remoteAction.Repo), action) {
				sar.remoteAction.URL = "https://github.com"
				github.Token = sar.RunContext.Config.ReplaceGheActionTokenWithGithubCom
			}
		}
		if sar.RunContext.Config.ActionCache != nil {
			cache := sar.RunContext.Config.ActionCache

			var err error
			sar.cacheDir = fmt.Sprintf("%s/%s", sar.remoteAction.Org, sar.remoteAction.Repo)
			repoURL := sar.remoteAction.URL + "/" + sar.cacheDir
			repoRef := sar.remoteAction.Ref
			sar.resolvedSha, err = cache.Fetch(ctx, sar.cacheDir, repoURL, repoRef, github.Token)
			if err != nil {
				return fmt.Errorf("failed to fetch \"%s\" version \"%s\": %w", repoURL, repoRef, err)
			}

			remoteReader := func(ctx context.Context) actionYamlReader {
				return func(filename string) (io.Reader, io.Closer, error) {
					spath := path.Join(sar.remoteAction.Path, filename)
					for range maxSymlinkDepth {
						tars, err := cache.GetTarArchive(ctx, sar.cacheDir, sar.resolvedSha, spath)
						if err != nil {
							return nil, nil, os.ErrNotExist
						}
						treader := tar.NewReader(tars)
						header, err := treader.Next()
						if err != nil {
							return nil, nil, os.ErrNotExist
						}
						if header.FileInfo().Mode()&os.ModeSymlink == os.ModeSymlink {
							spath, err = symlinkJoin(spath, header.Linkname, ".")
							if err != nil {
								return nil, nil, err
							}
						} else {
							return treader, tars, nil
						}
					}
					return nil, nil, fmt.Errorf("max depth %d of symlinks exceeded while reading %s", maxSymlinkDepth, spath)
				}
			}

			actionModel, err := sar.readAction(ctx, sar.Step, sar.resolvedSha, sar.remoteAction.Path, remoteReader(ctx), os.WriteFile)
			sar.action = actionModel
			return err
		}

		actionDir := fmt.Sprintf("%s/%s", sar.RunContext.ActionCacheDir(), sar.Step.UsesHash())
		defaultActionURL := sar.RunContext.Config.DefaultActionURL()
		token := getGitCloneToken(sar.getRunContext().Config, sar.remoteAction.CloneURL(defaultActionURL))
		gitClone := stepActionRemoteNewCloneExecutor(git.NewGitCloneExecutorInput{
			URL:         sar.remoteAction.CloneURL(defaultActionURL),
			Ref:         sar.remoteAction.Ref,
			Dir:         actionDir,
			Token:       token,
			OfflineMode: sar.RunContext.Config.ActionOfflineMode,

			InsecureSkipTLS: sar.cloneSkipTLS(), // For Gitea
		})
		var ntErr common.Executor
		if err := gitClone(ctx); err != nil {
			if errors.Is(err, git.ErrShortRef) {
				return fmt.Errorf("Unable to resolve action `%s`, the provided ref `%s` is the shortened version of a commit SHA, which is not supported. Please use the full commit SHA `%s` instead",
					sar.Step.Uses, sar.remoteAction.Ref, err.(*git.Error).Commit())
			} else if errors.Is(err, gogit.ErrForceNeeded) { // TODO: figure out if it will be easy to shadow/alias go-git err's
				ntErr = common.NewInfoExecutor("Non-terminating error while running 'git clone': %v", err)
			} else {
				return err
			}
		}

		remoteReader := func(ctx context.Context) actionYamlReader { //nolint:unparam // pre-existing issue from nektos/act
			return func(filename string) (io.Reader, io.Closer, error) {
				f, err := os.Open(filepath.Join(actionDir, sar.remoteAction.Path, filename))
				return f, f, err
			}
		}

		return common.NewPipelineExecutor(
			ntErr,
			func(ctx context.Context) error {
				defer git.AcquireCloneLock(actionDir)()
				actionModel, err := sar.readAction(ctx, sar.Step, actionDir, sar.remoteAction.Path, remoteReader(ctx), os.WriteFile)
				sar.action = actionModel
				return err
			},
		)(ctx)
	}
}

func (sar *stepActionRemote) pre() common.Executor {
	sar.env = map[string]string{}

	return common.NewPipelineExecutor(
		sar.prepareActionExecutor(),
		runStepExecutor(sar, stepStagePre, runPreStep(sar)).If(hasPreStep(sar)).If(shouldRunPreStep(sar)))
}

func (sar *stepActionRemote) main() common.Executor {
	return common.NewPipelineExecutor(
		sar.prepareActionExecutor(),
		runStepExecutor(sar, stepStageMain, func(ctx context.Context) error {
			printRunActionHeader(ctx, sar.Step, sar.env, sar.RunContext)
			rawLogger := common.Logger(ctx).WithField(rawOutputField, true)
			defer rawLogger.Infof("::endgroup::")

			github := sar.getGithubContext(ctx)
			if sar.remoteAction.IsCheckout() && isLocalCheckout(github, sar.Step) && !sar.RunContext.Config.NoSkipCheckout {
				if sar.RunContext.Config.BindWorkdir {
					common.Logger(ctx).Debugf("Skipping local actions/checkout because you bound your workspace")
					return nil
				}
				eval := sar.RunContext.NewExpressionEvaluator(ctx)
				copyToPath := path.Join(sar.RunContext.JobContainer.ToContainerPath(sar.RunContext.Config.Workdir), eval.Interpolate(ctx, sar.Step.With["path"]))
				return sar.RunContext.JobContainer.CopyDir(copyToPath, sar.RunContext.Config.Workdir+string(filepath.Separator)+".", sar.RunContext.Config.UseGitIgnore)(ctx)
			}

			actionDir := fmt.Sprintf("%s/%s", sar.RunContext.ActionCacheDir(), sar.Step.UsesHash())

			return sar.runAction(sar, actionDir, sar.remoteAction)(ctx)
		}),
	)
}

func (sar *stepActionRemote) post() common.Executor {
	return runStepExecutor(sar, stepStagePost, runPostStep(sar)).If(hasPostStep(sar)).If(shouldRunPostStep(sar))
}

func (sar *stepActionRemote) getRunContext() *RunContext {
	return sar.RunContext
}

func (sar *stepActionRemote) getGithubContext(ctx context.Context) *model.GithubContext {
	ghc := sar.getRunContext().getGithubContext(ctx)

	// extend github context if we already have an initialized remoteAction
	remoteAction := sar.remoteAction
	if remoteAction != nil {
		ghc.ActionRepository = fmt.Sprintf("%s/%s", remoteAction.Org, remoteAction.Repo)
		ghc.ActionRef = remoteAction.Ref
	}

	return ghc
}

func (sar *stepActionRemote) getStepModel() *model.Step {
	return sar.Step
}

func (sar *stepActionRemote) getEnv() *map[string]string {
	return &sar.env
}

func (sar *stepActionRemote) getIfExpression(ctx context.Context, stage stepStage) string {
	switch stage {
	case stepStagePre:
		github := sar.getGithubContext(ctx)
		if sar.remoteAction.IsCheckout() && isLocalCheckout(github, sar.Step) && !sar.RunContext.Config.NoSkipCheckout {
			// skip local checkout pre step
			return "false"
		}
		return sar.action.Runs.PreIf
	case stepStageMain:
		return sar.Step.If.Value
	case stepStagePost:
		return sar.action.Runs.PostIf
	}
	return ""
}

func (sar *stepActionRemote) getActionModel() *model.Action {
	return sar.action
}

func (sar *stepActionRemote) getCompositeRunContext(ctx context.Context) *RunContext {
	if sar.compositeRunContext == nil {
		actionDir := fmt.Sprintf("%s/%s", sar.RunContext.ActionCacheDir(), sar.Step.UsesHash())
		actionLocation := path.Join(actionDir, sar.remoteAction.Path)
		_, containerActionDir := getContainerActionPaths(sar.getStepModel(), actionLocation, sar.RunContext)

		sar.compositeRunContext = newCompositeRunContext(ctx, sar.RunContext, sar, containerActionDir)
		sar.compositeSteps = sar.compositeRunContext.compositeExecutor(sar.action)
	} else {
		// Re-evaluate environment here. For remote actions the environment
		// need to be re-created for every stage (pre, main, post) as there
		// might be required context changes (inputs/outputs) while the action
		// stages are executed. (e.g. the output of another action is the
		// input for this action during the main stage, but the env
		// was already created during the pre stage)
		env := evaluateCompositeInputAndEnv(ctx, sar.RunContext, sar)
		sar.compositeRunContext.Env = env
		sar.compositeRunContext.ExtraPath = sar.RunContext.ExtraPath
	}
	return sar.compositeRunContext
}

func (sar *stepActionRemote) getCompositeSteps() *compositeSteps {
	return sar.compositeSteps
}

// For Gitea
// cloneSkipTLS returns true if the runner can clone an action from the Gitea instance
func (sar *stepActionRemote) cloneSkipTLS() bool {
	if !sar.RunContext.Config.InsecureSkipTLS {
		// Return false if the Gitea instance is not an insecure instance
		return false
	}
	if sar.remoteAction.URL == "" {
		// Empty URL means the default action instance should be used
		// Return true if the URL of the Gitea instance is the same as the URL of the default action instance
		return sar.RunContext.Config.DefaultActionURL() == sar.RunContext.Config.GitHubInstance
	}
	// Return true if the URL of the remote action is the same as the URL of the Gitea instance
	return sar.remoteAction.URL == sar.RunContext.Config.GitHubInstance
}

type remoteAction struct {
	URL  string
	Org  string
	Repo string
	Path string
	Ref  string
}

func (ra *remoteAction) CloneURL(u string) string {
	if ra.URL == "" {
		// keep an absolute local path as-is (used by tests to resolve actions from a local
		// repo); only bare host names get the https:// scheme prepended
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") && !filepath.IsAbs(u) {
			u = "https://" + u
		}
	} else {
		u = ra.URL
	}

	return fmt.Sprintf("%s/%s/%s", u, ra.Org, ra.Repo)
}

func (ra *remoteAction) IsCheckout() bool {
	if ra.Org == "actions" && ra.Repo == "checkout" {
		return true
	}
	return false
}

func newRemoteAction(action string) *remoteAction {
	// support http(s)://host/owner/repo@v3
	for _, schema := range []string{"https://", "http://"} {
		if after, ok := strings.CutPrefix(action, schema); ok {
			splits := strings.SplitN(after, "/", 2)
			if len(splits) != 2 {
				return nil
			}
			ret := parseAction(splits[1])
			if ret == nil {
				return nil
			}
			ret.URL = schema + splits[0]
			return ret
		}
	}

	return parseAction(action)
}

func parseAction(action string) *remoteAction {
	// GitHub's document[^] describes:
	// > We strongly recommend that you include the version of
	// > the action you are using by specifying a Git ref, SHA, or Docker tag number.
	// Actually, the workflow stops if there is the uses directive that hasn't @ref.
	// [^]: https://docs.github.com/en/actions/reference/workflow-syntax-for-github-actions
	r := regexp.MustCompile(`^([^/@]+)/([^/@]+)(/([^@]*))?(@(.*))?$`)
	matches := r.FindStringSubmatch(action)
	if len(matches) < 7 || matches[6] == "" {
		return nil
	}
	return &remoteAction{
		Org:  matches[1],
		Repo: matches[2],
		Path: matches[4],
		Ref:  matches[6],
		URL:  "",
	}
}

func safeFilename(s string) string {
	return strings.NewReplacer(
		`<`, "-",
		`>`, "-",
		`:`, "-",
		`"`, "-",
		`/`, "-",
		`\`, "-",
		`|`, "-",
		`?`, "-",
		`*`, "-",
	).Replace(s)
}
