import { extensionOf } from './media-utils'

const EDITABLE_EXTENSIONS: Record<string, true> = {
  '4th': true, adoc: true, asc: true, asciidoc: true, asm: true, asn: true, asn1: true,
  bash: true, bat: true, bzl: true,
  c: true, 'c++': true, cc: true, cfg: true, cl: true, clj: true, cljc: true, cljs: true, cmake: true, cob: true, coffee: true, conf: true, cpp: true, cpy: true, cr: true, cs: true, cql: true, css: true, csv: true, cts: true, cxx: true, cypher: true,
  d: true, dart: true, diff: true, dockerfile: true, dtd: true, dyl: true, dylan: true,
  e: true, ecl: true, edn: true, elm: true, env: true, erl: true, ex: true, exs: true,
  f: true, f77: true, f90: true, f95: true, factor: true, feature: true, fish: true, for: true, forth: true, fs: true, fsx: true, fth: true,
  gemspec: true, gql: true, gradle: true, graphql: true, groovy: true, gss: true,
  h: true, 'h++': true, handlebars: true, hbs: true, hh: true, hpp: true, hs: true, htm: true, html: true, hxx: true, hxml: true,
  in: true, ini: true, ino: true,
  j2: true, jade: true, java: true, jinja: true, jinja2: true, jl: true, js: true, json: true, json5: true, jsonl: true, jsonld: true, jsx: true,
  kt: true, kts: true,
  less: true, liquid: true, lisp: true, log: true, lua: true,
  m: true, makefile: true, markdown: true, md: true, mkd: true, ml: true, mli: true, mll: true, mly: true, mo: true, mps: true, mrc: true, mts: true,
  nb: true, ndjson: true, nginx: true, nix: true, nq: true, nsh: true, nsi: true, nt: true,
  org: true,
  p: true, pas: true, patch: true, pgp: true, php: true, php3: true, php4: true, php5: true, php7: true, pig: true, pl: true, pm: true, powershell: true, properties: true, proto: true, ps1: true, psd1: true, psm1: true, pug: true, py: true, pyw: true, pyx: true,
  q: true,
  r: true, rb: true, rkt: true, rs: true, rst: true,
  s: true, sass: true, scala: true, scheme: true, scss: true, sh: true, sieve: true, sig: true, sol: true, spec: true, sql: true, ss: true, svelte: true, svg: true, swift: true,
  tcl: true, tex: true, text: true, tf: true, tfvars: true, toml: true, ts: true, tsv: true, tsx: true, turtle: true, txt: true,
  vb: true, vbs: true, vhd: true, vhdl: true, vim: true, vue: true,
  wat: true, wast: true, wl: true, wls: true,
  xhtml: true, xml: true, xq: true, xql: true, xqm: true, xquery: true, xsd: true, xsl: true,
  yaml: true, yml: true,
  zsh: true
}

const EDITABLE_NAMES: Record<string, true> = {
  '.editorconfig': true, '.gitattributes': true, '.gitignore': true, '.npmrc': true, '.prettierrc': true,
  build: true, buck: true, 'cmakelists.txt': true, dockerfile: true, gemfile: true, jenkinsfile: true,
  makefile: true, procfile: true, rakefile: true, vagrantfile: true
}

export function isEditableFileName(name: string): boolean {
  const basename = name.split('/').at(-1)?.toLowerCase() ?? ''
  if (EDITABLE_NAMES[basename] || basename.startsWith('.env.') || basename.startsWith('dockerfile.') || basename.startsWith('makefile.')) return true
  return Boolean(EDITABLE_EXTENSIONS[extensionOf(basename)])
}
