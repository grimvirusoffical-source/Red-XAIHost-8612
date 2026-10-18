'use strict';
// Decoder for the Box text the host returns (GRAMMAR.md section 2) into plain
// JavaScript values. It follows the host's JSON mapping (GRAMMAR.md section 5):
//   string/number/TRUE/FALSE -> string/number/boolean, NELL -> null,
//   ;[a, b]: -> array, [ {K[]} = v, {K[ID=2]} = w ] -> { K: v, "K[ID=2]": w },
//   link -> { "$link": "<link text>" }.
// TTL markers after a value are skipped. The decoder is strict: anything it
// does not understand throws a BoxTextError carrying the offset.

class BoxTextError extends Error {
  constructor(message, offset) {
    super(`${message} (at offset ${offset})`);
    this.name = 'BoxTextError';
    this.offset = offset;
  }
}

const MAX_DEPTH = 200;

function isIdentStart(c) { return /[A-Za-z_]/.test(c) || c > '\u007f'; }
function isIdentPart(c) { return /[A-Za-z0-9_-]/.test(c) || c > '\u007f'; }

class Reader {
  constructor(src) { this.s = src; this.i = 0; }
  fail(msg) { throw new BoxTextError(msg, this.i); }
  ws() {
    for (;;) {
      const c = this.s[this.i];
      if (c === ' ' || c === '\t' || c === '\n' || c === '\r') this.i++;
      else if (c === '<' && this.s[this.i + 1] === '<') {
        const end = this.s.indexOf('>>', this.i + 2);
        if (end < 0) this.fail('unterminated comment');
        this.i = end + 2;
      } else return;
    }
  }
  peek() { this.ws(); return this.s[this.i]; }
  eat(ch) { if (this.peek() === ch) { this.i++; return true; } return false; }
  expect(ch) { if (!this.eat(ch)) this.fail(`expected '${ch}'`); }

  string() {
    const start = this.i;
    if (this.s[this.i] !== '"') this.fail('expected a string');
    this.i++;
    for (;;) {
      const c = this.s[this.i];
      if (c === undefined) this.fail('unterminated string');
      if (c === '\\') { this.i += 2; continue; }
      this.i++;
      if (c === '"') break;
    }
    const lit = this.s.slice(start, this.i);
    try { return JSON.parse(lit); } catch (e) { this.i = start; this.fail('bad string literal'); }
    return null;
  }

  number() {
    const m = /^-?\d+(\.\d+)?([eE][+-]?\d+)?/.exec(this.s.slice(this.i, this.i + 400));
    if (!m) this.fail('expected a number');
    this.i += m[0].length;
    return Number(m[0]);
  }

  ident() {
    const st = this.i;
    if (!isIdentStart(this.s[this.i] || ' ')) this.fail('expected an identifier');
    while (this.i < this.s.length && isIdentPart(this.s[this.i])) this.i++;
    return this.s.slice(st, this.i);
  }

  ttl() {
    if (this.peek() !== '(') return;
    const end = this.s.indexOf(')', this.i);
    if (end < 0 || !/^\(\s*~?\d+(@\d+)?\s*$/.test(this.s.slice(this.i, end))) this.fail('bad TTL marker');
    this.i = end + 1;
    if (this.s[this.i] === '*') this.i++;
  }

  // a link starts with a string followed by ':' ident; capture its text up to the next delimiter
  linkText(start) {
    let depth = 0;
    this.i = start;
    for (;;) {
      const c = this.s[this.i];
      if (c === undefined) break;
      if (c === '"') { this.string(); continue; }
      if (c === '[') depth++;
      else if (c === ']' || c === '}') { if (depth === 0) break; depth--; }
      else if ((c === ',' || c === ';') && depth === 0) break;
      this.i++;
    }
    return this.s.slice(start, this.i).trim();
  }

  value(depth) {
    if (depth > MAX_DEPTH) this.fail('nested too deeply');
    const c = this.peek();
    if (c === undefined) this.fail('unexpected end of input');
    if (c === '"') {
      const start = this.i;
      const str = this.string();
      let j = this.i;
      while (/\s/.test(this.s[j] || '')) j++;
      if (this.s[j] === ':' && isIdentStart(this.s[j + 1] || ' ')) {
        return { $link: this.linkText(start) };
      }
      return str;
    }
    if (c === ';') {
      this.i++;
      this.expect('[');
      const arr = [];
      if (!this.eat(']')) {
        do { arr.push(this.value(depth + 1)); } while (this.eat(','));
        this.expect(']');
      }
      this.expect(':');
      return arr;
    }
    if (c === '[') { this.i++; return this.boxBody(depth + 1); }
    if (c === '-' || (c >= '0' && c <= '9')) return this.number();
    if (isIdentStart(c)) {
      const w = this.ident().toUpperCase();
      if (w === 'TRUE') return true;
      if (w === 'FALSE') return false;
      if (w === 'NELL') return null;
      this.fail(`unexpected word ${w}`);
    }
    this.fail('unexpected character');
    return null;
  }

  boxBody(depth) {
    const out = {};
    while (!this.eat(']')) {
      this.expect('{');
      const name = this.ident();
      let id = null;
      if (this.eat('[')) {
        while (!this.eat(']')) {
          this.ws();
          const m = /^(?:ID\s*=\s*)?(-?\d+)\s*(?=[,\]])/i.exec(this.s.slice(this.i, this.i + 40));
          if (m && id === null) { id = m[1]; this.i += m[0].length; }
          else { // other attributes (LOCK=3, ON=...) are skipped up to the next ',' or ']'
            while (this.i < this.s.length && this.s[this.i] !== ',' && this.s[this.i] !== ']') {
              if (this.s[this.i] === '"') this.string(); else this.i++;
            }
          }
          this.eat(',');
        }
      }
      this.expect('}');
      this.expect('=');
      const v = this.value(depth + 1);
      this.ttl();
      // defineProperty so a field called __proto__ stays plain data
      Object.defineProperty(out, id === null ? name : `${name}[ID=${id}]`,
        { value: v, enumerable: true, writable: true, configurable: true });
      if (!this.eat(',')) this.eat(';');
    }
    return out;
  }
}

/** Decode the Box text of one value (a scalar, an array, or a box `[...]`). */
function fromBox(text) {
  if (typeof text !== 'string') throw new TypeError('fromBox needs a string');
  const r = new Reader(text);
  const v = r.value(0);
  r.ttl();
  if (r.peek() !== undefined) r.fail('unexpected text after the value');
  return v;
}

module.exports = { fromBox, BoxTextError };
