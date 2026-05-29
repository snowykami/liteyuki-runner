import {execFileSync} from 'node:child_process';

// Run the `main` input as a bash script; its stdout (workflow commands like
// ::set-output / ::save-state) and $GITHUB_ENV / $GITHUB_STATE writes are
// processed by the runner, exactly like the remote script action this replaces.
const script = process.env.INPUT_MAIN;
if (script) {
  execFileSync('bash', ['-eo', 'pipefail', '-c', script], {stdio: 'inherit'});
}
