// Why: the SpaRewrite CloudFront Function is the only thing that sends the old CloudFront address to the
// app's hostname, and it lives as a YAML literal inside each template, where nothing else runs it. This test
// pulls that literal out, fills the one !Sub variable the way CloudFormation does, and runs `handler`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const infra = join(import.meta.dirname, '../..');

// Returns the JS under `SpaRewrite: ... FunctionCode: !Sub` / `- |` (the first list item of the Sub).
function extractFunctionCode(file) {
  const lines = readFileSync(join(infra, file), 'utf8').split('\n');
  const at = (re, from) => {
    const i = lines.findIndex((l, n) => n >= from && re.test(l));
    assert.notEqual(i, -1, `${file}: ${re}`);
    return i;
  };
  const res = at(/^ {2}SpaRewrite:$/, 0);
  const code = at(/^ {6}FunctionCode: !Sub$/, res);
  const start = at(/^ {8}- \|$/, code) + 1;
  const body = [];
  for (let i = start; i < lines.length; i++) {
    if (lines[i] !== '' && !lines[i].startsWith(' '.repeat(10))) break;
    body.push(lines[i].slice(10));
  }
  const js = body.join('\n');
  assert.ok(js.includes('${Hostname}'), `${file}: the Hostname placeholder`);
  assert.equal(js.replaceAll('${Hostname}', '').includes('${'), false, `${file}: no other \${ in the code`);
  return js;
}

function handlerFor(file, hostname) {
  const js = extractFunctionCode(file).replaceAll('${Hostname}', hostname);
  return new Function(`${js}\nreturn handler;`)();
}

const event = (host, uri, querystring = {}) => ({ request: { uri, querystring, headers: { host: { value: host } } } });

for (const file of ['console.yaml', 'portal.yaml']) {
  test(`${file}: another host is redirected, path and every query value kept`, () => {
    const out = handlerFor(file, 'admin.authgatehq.com')(
      event('dxxx.cloudfront.net', '/clients/x', {
        a: { value: '1', multiValue: [{ value: '1' }, { value: '2' }] },
        q: { value: 'a b' },
      }),
    );
    assert.equal(out.statusCode, 301);
    assert.equal(out.statusDescription, 'Moved Permanently');
    assert.equal(out.headers.location.value, 'https://admin.authgatehq.com/clients/x?a=1&a=2&q=a%20b');
  });

  test(`${file}: a query value comes out percent-encoded once, whether it arrives encoded or decoded`, () => {
    const h = handlerFor(file, 'admin.authgatehq.com');
    for (const value of ['a%20b', 'a b']) {
      const out = h(event('dxxx.cloudfront.net', '/', { q: { value } }));
      assert.equal(out.headers.location.value, 'https://admin.authgatehq.com/?q=a%20b', value);
    }
  });

  test(`${file}: a malformed percent sequence stays encoded and does not throw`, () => {
    const out = handlerFor(file, 'admin.authgatehq.com')(event('dxxx.cloudfront.net', '/', { q: { value: '%E0%A4%A' } }));
    assert.equal(out.headers.location.value, 'https://admin.authgatehq.com/?q=%25E0%25A4%25A');
  });

  test(`${file}: no query string gives no question mark`, () => {
    const out = handlerFor(file, 'admin.authgatehq.com')(event('dxxx.cloudfront.net', '/'));
    assert.equal(out.headers.location.value, 'https://admin.authgatehq.com/');
  });

  test(`${file}: the app host still gets the SPA rewrite`, () => {
    const out = handlerFor(file, 'admin.authgatehq.com')(event('admin.authgatehq.com', '/clients/x'));
    assert.equal(out.statusCode, undefined);
    assert.equal(out.uri, '/index.html');
  });

  test(`${file}: an asset on the app host is unchanged`, () => {
    const out = handlerFor(file, 'admin.authgatehq.com')(event('admin.authgatehq.com', '/assets/a.js'));
    assert.equal(out.uri, '/assets/a.js');
  });

  test(`${file}: an empty hostname never redirects`, () => {
    const h = handlerFor(file, '');
    for (const host of ['dxxx.cloudfront.net', 'admin.authgatehq.com']) {
      const out = h(event(host, '/clients/x'));
      assert.equal(out.statusCode, undefined, host);
      assert.equal(out.uri, '/index.html', host);
    }
  });
}
