import {execFileSync} from 'node:child_process';

const script = process.env.INPUT_POST;
if (script) {
  execFileSync('bash', ['-eo', 'pipefail', '-c', script], {stdio: 'inherit'});
}
