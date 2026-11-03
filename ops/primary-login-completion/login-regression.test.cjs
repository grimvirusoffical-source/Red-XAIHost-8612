'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');
const { EXPECTED_INPUT_SHA256, extractFunction, patchBundle, sha256 } = require('./patch-login.cjs');

const original = fs.readFileSync(path.join(__dirname, 'fixtures', 'index-customers-console-aeeeebb35bec.js'));
const { output, report } = patchBundle(original);
const originalSource = original.toString('utf8');
const patchedSource = output.toString('utf8');

// Run the actual downloaded pN and its existing tg completion hook in a VM.
// No network, production cookies, account records or credentials are used.
function mount(source, response, authoritativeSignedIn = false) {
  const events = { requests: [], views: [], errors: [], busy: [], history: [], invalidations: [], meReads: 0, signedIn: false };
  let stateIndex = 0;
  const initial = ['  member@example.invalid  ', 'synthetic-test-password', false, ''];
  const jsx = (type, props) => ({ type, props });
  const sandbox = {
    C: {
      useState() {
        const index = stateIndex++;
        return [initial[index], value => {
          if (index === 2) events.busy.push(value);
          if (index === 3) events.errors.push(value);
        }];
      },
    },
    l: { jsx, jsxs: jsx },
    mn: 'AuthFrame', bt: 'Field', ku: 'Password', un: 'Error', $: 'Button', de: 'Spinner', la: 'input',
    async St(route, body) {
      events.requests.push({ route, body });
      return response;
    },
    Lx: () => '/hub',
    window: { history: { replaceState(...args) { events.history.push(args); } } },
    Yt: () => ({
      invalidateQueries(options) {
        events.invalidations.push(JSON.parse(JSON.stringify(options)));
        events.meReads++;
        // Only this simulated authoritative /me reply controls signed-in state.
        events.signedIn = authoritativeSignedIn;
      },
    }),
  };
  vm.createContext(sandbox);
  const completion = extractFunction(source, 'tg', 'pN').source;
  const component = extractFunction(source, 'pN', 'Up').source;
  vm.runInContext(completion + component + ';this.mount=pN;', sandbox);
  const tree = sandbox.mount({ setView: view => events.views.push(JSON.parse(JSON.stringify(view))) });
  const form = tree.props.children;
  assert.equal(form.type, 'form');
  return {
    events,
    async submit() {
      let prevented = false;
      await form.props.onSubmit({ preventDefault() { prevented = true; } });
      assert.equal(prevented, true);
      return events;
    },
  };
}

test('baseline reproduces direct-success stall; patched component only revalidates authoritative /me', async () => {
  const baseline = mount(originalSource, { status: 200, json: {} }, true);
  await baseline.submit();
  assert.equal(baseline.events.meReads, 0);
  assert.equal(baseline.events.signedIn, false);

  for (const authoritativeSignedIn of [false, true]) {
    const patched = mount(patchedSource, { status: 200, json: {} }, authoritativeSignedIn);
    const events = await patched.submit();
    assert.equal(events.meReads, 1);
    assert.deepEqual(events.invalidations, [{ queryKey: ['me'] }]);
    assert.equal(events.signedIn, authoritativeSignedIn);
    assert.equal(events.views.length, 0);
    assert.deepEqual(events.history, [[null, '', '/hub']]);
    assert.deepEqual(events.requests.map(x => x.route), ['login']);
    assert.equal(events.requests[0].body.login, 'member@example.invalid');
    assert.deepEqual(events.busy, [true, false]);
  }
});

test('empty next and empty pending are accepted as direct completion', async () => {
  const { events, submit } = mount(patchedSource, { status: 200, json: { next: '', pending: [] } });
  await submit();
  assert.equal(events.meReads, 1);
  assert.equal(events.signedIn, false);
  assert.equal(events.views.length, 0);
});

test('active MFA challenge is preserved and never completed early', async () => {
  const response = { status: 200, json: { next: '2fa', token: 'synthetic-flow', methods: { totp: true, call: false } } };
  const { events, submit } = mount(patchedSource, response, true);
  await submit();
  assert.deepEqual(events.views, [{ k: 'challenge', token: 'synthetic-flow', methods: { totp: true, call: false } }]);
  assert.equal(events.meReads, 0);
  assert.equal(events.signedIn, false);
  assert.equal(events.history.length, 0);
});

test('MFA enrollment remains required when backend requests it', async () => {
  const response = { status: 200, json: { next: '2fa_setup', token: 'synthetic-enrollment', methods: { call: false }, username: 'fixture-user' } };
  const { events, submit } = mount(patchedSource, response, true);
  await submit();
  assert.deepEqual(events.views, [{ k: 'setup', token: 'synthetic-enrollment', methods: { call: false }, username: 'fixture-user' }]);
  assert.equal(events.meReads, 0);
  assert.equal(events.signedIn, false);
});

for (const [status, message] of [[401, 'Invalid username or password.'], [403, 'Email needs activated.'], [0, 'RedX is not reachable.']]) {
  test(`HTTP ${status} error remains visible without completing sign-in`, async () => {
    const { events, submit } = mount(patchedSource, { status, json: { message } }, true);
    await submit();
    assert.equal(events.errors.at(-1), message);
    assert.equal(events.views.length, 0);
    assert.equal(events.meReads, 0);
    assert.equal(events.signedIn, false);
    assert.deepEqual(events.busy, [true, false]);
  });
}

for (const json of [
  { next: 'email_activation' },
  { next: null },
  { next: false },
  { token: 'synthetic-flow' },
  { challenge: 'synthetic-flow' },
  { token: null },
  { error: 'activation_required' },
  { pending: ['2fa_setup'] },
  { pending: '2fa_setup' },
]) {
  test(`unknown or unresolved successful response fails visibly: ${JSON.stringify(json)}`, async () => {
    const { events, submit } = mount(patchedSource, { status: 200, json }, true);
    await submit();
    assert.equal(events.errors.at(-1), 'Sign-in did not complete. Please try again.');
    assert.equal(events.views.length, 0);
    assert.equal(events.meReads, 0);
    assert.equal(events.signedIn, false);
  });
}

test('exact input pin and patch isolation protect every other function', () => {
  assert.equal(sha256(original), EXPECTED_INPUT_SHA256);
  assert.deepEqual(output, fs.readFileSync(path.join(__dirname, 'fixtures', 'index-customers-console-81b28895ccc8.js')));
  const before = extractFunction(originalSource, 'pN', 'Up');
  const after = extractFunction(patchedSource, 'pN', 'Up');
  assert.equal(originalSource.slice(0, before.start), patchedSource.slice(0, after.start));
  assert.equal(originalSource.slice(before.end), patchedSource.slice(after.end));
  assert.equal(report.replacements, 2);
  assert.deepEqual(report.changedFunctions, ['pN']);
  assert.throws(() => patchBundle(Buffer.concat([original, Buffer.from('\n')])), /SHA-256 mismatch/);
  assert.throws(() => patchBundle(output), /SHA-256 mismatch/);
});
