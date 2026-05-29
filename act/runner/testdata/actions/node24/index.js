import {appendFileSync, readFileSync} from 'node:fs';

const nameToGreet = process.env['INPUT_WHO-TO-GREET'] || 'World';
console.log(`Hello ${nameToGreet}!`);

if (process.env.GITHUB_OUTPUT) {
  appendFileSync(process.env.GITHUB_OUTPUT, `time=${new Date().toTimeString()}\n`);
}

let payload = {};
if (process.env.GITHUB_EVENT_PATH) {
  payload = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'));
}
console.log(`The event payload: ${JSON.stringify(payload, undefined, 2)}`);
