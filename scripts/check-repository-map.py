#!/usr/bin/env python3
"""Check directory responsibilities, named paths and the retired-module boundary.

Classifications are editorial. Descendants inherit their directory's role; only
new top-level areas or ownership packages need map changes. This is not a file,
caller or embed inventory: embed.go and the Go compiler own build membership.
The tracked path set comes from git ls-files; contents come from the worktree.
Run with no arguments to check the map, or --selftest for independent fixtures.
"""
from __future__ import annotations

import pathlib
import posixpath
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
MAP = 'docs/repository-map.md'


class Anchor(Exception):
    """A missing input or extraction anchor must fail, not silently check nothing."""


def worktree(start: pathlib.Path) -> pathlib.Path:
    # Mutation mirrors symlink docs/ back to the repository, but copy the checker.
    for where in (start, (start / MAP).resolve().parent):
        got = subprocess.run(['git', '-C', str(where), 'rev-parse', '--show-toplevel'],
                             capture_output=True, text=True)
        if got.returncode == 0 and got.stdout.strip():
            return pathlib.Path(got.stdout.strip())
    return start


class Tree:
    def __init__(self, root: pathlib.Path, files: list[str] | None = None):
        self.root = root if files is not None else worktree(root)
        if files is None:
            got = subprocess.run(['git', 'ls-files', '-z'], cwd=self.root, capture_output=True)
            if got.returncode:
                raise Anchor('git ls-files failed: ' + got.stderr.decode(errors='replace'))
            files = [p for p in got.stdout.decode().split('\0') if p]
        self.files = sorted(files)
        if not self.files:
            raise Anchor('the tracked tree is empty')

    def under(self, prefix: str) -> set[str]:
        return {p for p in self.files if p.startswith(prefix.rstrip('/') + '/')}

    def read(self, path: str) -> str:
        if path not in self.files:
            raise Anchor(f'required input {path} is not tracked')
        return (self.root / path).read_text(encoding='utf-8')


class Doc:
    def __init__(self, text: str):
        if not text.strip():
            raise Anchor('the map is empty')
        parts = re.split(r'^## ', text, flags=re.M)
        self.preamble = parts[0]
        self.sections = [tuple(part.partition('\n')[::2]) for part in parts[1:]]

    def section(self, needle: str) -> str:
        hits = [body for heading, body in self.sections if needle in heading]
        if len(hits) != 1 or not hits[0].strip():
            raise Anchor(f'expected one nonempty section for {needle}')
        return hits[0]


def ticks(text: str) -> list[str]:
    return re.findall(r'`([^`\n]+)`', text)


def is_path_candidate(token: str) -> bool:
    if not re.fullmatch(r'[A-Za-z0-9_.*/-]+', token) or token.startswith('-'):
        return False
    if re.fullmatch(r'\.[a-z]+', token) or re.search(r'(^|/)\.{2,}(/|$)', token):
        return False  # File kinds and plane/... import families are not local paths.
    return ('/' in token or '.' in token.strip('.')
            or token in {'Makefile', 'LICENSE', 'README', 'Dockerfile'})


def resolve(token: str, base: str, tree: Tree) -> set[str]:
    import fnmatch

    # Explicit root-qualified paths must not fall back to unrelated suffixes.
    tops = {p.split('/')[0] for p in tree.files if '/' in p}
    candidates = [token.rstrip('/')]
    if base and token.split('/')[0] not in tops:
        candidates.insert(0, base.rstrip('/') + '/' + token.rstrip('/'))
    for candidate in candidates:
        found = {p for p in tree.files if p == candidate or p.startswith(candidate + '/')
                 or (p.count('/') == candidate.count('/') and fnmatch.fnmatchcase(p, candidate))}
        if found:
            return found
    # A short source example such as lift*.go is scoped to its section, not global.
    if base and '/' not in token:
        return {p for p in tree.under(base) if fnmatch.fnmatchcase(pathlib.PurePosixPath(p).name, token)}
    return set()


CLAIMS: list = []


def claim(fn):
    CLAIMS.append(fn)
    return fn


@claim
def every_area_of_the_tree_has_a_section(doc: Doc, tree: Tree) -> list[str]:
    areas = {p.split('/')[0] for p in tree.files if '/' in p}
    if not areas:
        raise Anchor('no top-level directories found')
    problems = []
    for area in sorted(areas):
        bodies = [body for heading, body in doc.sections if area + '/' in ticks(heading)]
        if len(bodies) != 1 or not bodies[0].strip():
            problems.append(f'{area}/ needs one nonempty top-level responsibility section')
    doc.section('`testdata/agentsuite/`')
    doc.section('Root files')
    return problems


@claim
def every_path_the_map_names_is_in_the_tree(doc: Doc, tree: Tree) -> list[str]:
    problems, examined = [], 0
    for heading, body in [('', doc.preamble)] + doc.sections:
        directories = [t for t in ticks(heading) if t.endswith('/')]
        base = directories[0] if len(directories) == 1 else ''
        for token in ticks(heading + '\n' + body):
            if not is_path_candidate(token):
                continue
            examined += 1
            if not resolve(token, base, tree):
                problems.append(f'{heading or "opening section"}: named path {token!r} does not resolve')
        for target in re.findall(r'\]\(([^\s)]+)(?:\s+"[^"]*")?\)', body):
            if target.startswith('#') or re.match(r'[a-zA-Z][\w+.-]*:', target):
                continue
            examined += 1
            path = target.split('#')[0]
            if path.startswith('/') or not resolve(posixpath.normpath('docs/' + path), '', tree):
                problems.append(f'local link {target!r} does not resolve')
    if not examined:
        raise Anchor('no named paths were extracted')
    return problems


@claim
def directory_responsibilities_cover_packages(doc: Doc, tree: Tree) -> list[str]:
    problems = []
    for area in ('cmd', 'internal', 'pkg'):
        body = doc.section(f'`{area}/`')
        rows = re.findall(r'^\| `([^`]+)` \|([^|\n]*)\|([^|\n]*)\|\s*$', body, re.M)
        if not rows:
            raise Anchor(f'{area}/ has no directory responsibility rows')
        named = set()
        for path, classification, responsibility in rows:
            path = path.rstrip('/')
            shape = r'internal/(?:kmx/[^/]+|(?!kmx$)[^/]+)' if area == 'internal' else area + r'/[^/]+'
            if not re.fullmatch(shape, path) or not classification.strip() or not responsibility.strip():
                problems.append(f'invalid or empty {area}/ ownership row: {path}')
                continue
            if path in named:
                problems.append(f'duplicate ownership row: {path}')
            named.add(path)
            if not tree.under(path):
                problems.append(f'ownership directory is absent: {path}')
        for path in sorted(tree.under(area)):
            parts = path.split('/')
            depth = 3 if parts[:2] == ['internal', 'kmx'] else 2
            owner = '/'.join(parts[:depth])
            if len(parts) <= depth or owner not in named:
                problems.append(f'{path} has no directory responsibility for {owner}')
    return problems


def without_go_comments(text: str) -> str:
    # Preserve strings (including raw imports), so comment-like text inside a
    # string cannot conceal an actual import elsewhere in the declaration.
    return re.sub(r'`[^`]*`|"(?:\\.|[^"\\])*"|//[^\n]*|/\*.*?\*/',
                  lambda m: ' ' if m[0].startswith(('//', '/*')) else m[0], text, flags=re.S)


@claim
def the_root_module_does_not_import_the_plane(doc: Doc, tree: Tree) -> list[str]:
    body = doc.section('`internal/`')
    if not re.search(r'The\s+plane\s+module\s+and\s+image-build\s+helper\s+are\s+absent\.', body):
        raise Anchor('the retired-source boundary anchor is missing')
    module = 'github.com/kaimahi-agents/kaimahi/plane'
    problems = [f'retired source is tracked: {p}' for p in sorted(
        tree.under('plane') | tree.under('internal/kmx/planebuild'))]
    module_tokens = re.split(r'[\s()="]+', without_go_comments(tree.read('go.mod')))
    if any(t == module or t.startswith(module + '/') for t in module_tokens):
        problems.append('go.mod references the retired plane module')
    source = {p for p in tree.under('cmd') | tree.under('internal') if p.endswith('.go')}
    if not source:
        raise Anchor('no Go source inspected under cmd/ or internal/')
    for path in sorted(source):
        text = without_go_comments(tree.read(path))
        # Tokenize strings as units: quoted historical source is not a declaration.
        tokens = re.findall(r'`[^`]*`|"(?:\\.|[^"\\])*"|\w+|[().;]', text)
        for index, token in enumerate(tokens):
            if token != 'import':
                continue
            following = tokens[index + 1:]
            if following and following[0] == '(':
                following = following[1:following.index(')')] if ')' in following else []
            else:
                following = following[:2]  # Optional alias, then the import string.
            for literal in following:
                imported = literal[1:-1] if literal.startswith(('"', '`')) else ''
                if imported == module or imported.startswith(module + '/'):
                    problems.append(f'{path} imports the plane module: {imported}')
    return problems


def check(tree: Tree, map_text: str) -> tuple[list[str], int]:
    if not CLAIMS:
        return ['no claims registered; refusing a vacuous check'], 0
    try:
        doc = Doc(map_text)
    except Anchor as error:
        return [str(error)], 0
    problems = []
    for fn in CLAIMS:
        try:
            findings = fn(doc, tree)
        except (Anchor, OSError, UnicodeError) as error:
            findings = [str(error)]
        problems.extend(f'[{fn.__name__}] {finding}' for finding in findings)
    return problems, len(CLAIMS)


def exit_code(problems: list[str]) -> int:
    """The verdict. Tested separately so findings cannot be silently ignored."""
    return 1 if problems else 0


def main(argv) -> int:
    if argv == ['--selftest']:
        return selftest()
    if argv:
        print(f'check-repository-map: unexpected argument {argv[0]!r}', file=sys.stderr)
        return 2
    try:
        tree = Tree(ROOT)
        problems, ran = check(tree, tree.read(MAP))
    except (Anchor, OSError, UnicodeError) as error:
        problems, ran = [str(error)], 0
    for problem in problems:
        print('check-repository-map: ' + problem, file=sys.stderr)
    if not problems:
        print(f'check-repository-map: {ran} claim(s) checked against {len(tree.files)} tracked files, '
              'the map agrees with the tree')
    return exit_code(problems)


# Independent miniature repository: no live map prose or inventory is copied.
SELFTEST_FILES = {
    'go.mod': 'module example.invalid/fixture\n',
    'cmd/tool/main.go': 'package main\n',
    'internal/kmx/app/app.go': 'package app\n',
    'internal/support/util.go': 'package support\n',
    'pkg/view/view.go': 'package view\n',
    'scripts/check.py': 'pass\n',
    'docs/guide.md': '# Guide\n',
    'brand/mark.svg': '<svg/>\n',
    'testdata/agentsuite/minimal/input.json': '{}\n',
    'changes/README.md': '# Notes\n',
}
SELFTEST_MAP = '''# Fixture map
See `go.mod` and [the guide](guide.md).
## `cmd/` — commands
| Directory | Class | Responsibility |
|---|---|---|
| `cmd/tool` | Installed | Command adapter. |
## `internal/` — implementation
The plane module and image-build helper are absent.
| Directory | Class | Responsibility |
|---|---|---|
| `internal/kmx/app` | Installed | Operations and nested fixtures. |
| `internal/support` | Scaffolding | Shared support. |
## `pkg/` — libraries
| Directory | Class | Responsibility |
|---|---|---|
| `pkg/view` | Library | Read-only projections. |
## `scripts/` — tools
Checkout automation and its nested test support inherit this role.
## `docs/` — guidance
Maintained guides and historical design records, not runtime assets.
## `brand/` — identity
Repository identity assets.
## `testdata/` — owned snapshots (`testdata/agentsuite/`)
Consumer snapshots and provenance, not the external format implementation.
## `changes/` — notes
Per-PR release inputs, not rendered documentation.
## Root files
Build metadata and entry-point documentation.
'''


def selftest() -> int:
    import tempfile

    failures, cases, broken = [], 0, set()

    def exercise(label, text=SELFTEST_MAP, additions=None, missing=(), want=''):
        nonlocal cases
        cases += 1
        files = dict(SELFTEST_FILES)
        files.update(additions or {})
        files = {p: body for p, body in files.items() if p not in missing}
        with tempfile.TemporaryDirectory(prefix='kmx-map-selftest-') as tmp:
            root = pathlib.Path(tmp)
            for path, body in files.items():
                target = root / path
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(body, encoding='utf-8')
            problems, ran = check(Tree(root, list(files)), text)
        if want:
            ok = any(p.startswith(f'[{want}]') for p in problems)
            if ok:
                broken.add(want)
        else:
            ok = not problems and ran == len(CLAIMS) and ran > 0
        if not ok:
            failures.append(f'{label}: {problems} (evaluated {ran} claims)')

    area = 'every_area_of_the_tree_has_a_section'
    paths = 'every_path_the_map_names_is_in_the_tree'
    ownership = 'directory_responsibilities_cover_packages'
    retired = 'the_root_module_does_not_import_the_plane'
    exercise('valid miniature map')
    for path in ('internal/kmx/app/ordinary.go', 'scripts/ordinary.py',
                 'docs/reviews/ordinary.md', 'testdata/agentsuite/minimal/nested/input.json',
                 'cmd/tool/nested/fixture.txt', 'pkg/view/nested/data.json',
                 'internal/support/nested/fixture.json', 'brand/nested/new.svg',
                 'changes/999.md', 'new-root-config.txt'):
        exercise(f'inherited responsibility: {path}', additions={path: '// ordinary\n'})
    exercise('new top-level area', additions={'unowned/data.txt': ''}, want=area)
    exercise('new area with a responsibility', text=SELFTEST_MAP + '\n## `extra/` — examples\nCheckout examples.\n',
             additions={'extra/example.txt': ''})
    exercise('new package with an ownership row', text=SELFTEST_MAP.replace(
        '| `pkg/view` | Library | Read-only projections. |',
        '| `pkg/view` | Library | Read-only projections. |\n| `pkg/extra` | Library | Extra projection. |'),
        additions={'pkg/extra/view.go': 'package extra\n'})
    exercise('empty area', text=SELFTEST_MAP.replace(
        'Repository identity assets.', ''), want=area)
    exercise('missing required fixture anchor', text=SELFTEST_MAP.replace(
        ' (`testdata/agentsuite/`)', ''), want=area)
    exercise('missing root role', text=SELFTEST_MAP.replace(
        '## Root files\nBuild metadata and entry-point documentation.', ''), want=area)
    exercise('broken named path', text=SELFTEST_MAP + '\n`docs/missing.md`\n', want=paths)
    exercise('broken local link', text=SELFTEST_MAP.replace('(guide.md)', '(missing.md)'), want=paths)
    exercise('local link cannot fall back to root namesake',
             additions={'guide.md': '# Wrong guide', 'docs/other.md': '# Other'},
             missing=('docs/guide.md',), want=paths)
    exercise('missing path extraction', text='# Nothing named\n', want=paths)
    exercise('no suffix fallback for explicit paths', text=SELFTEST_MAP + '\n`wrong/docs/guide.md`', want=paths)
    exercise('valid relative names', text=SELFTEST_MAP.replace(
        'Maintained guides', '`guide.md` and maintained guides'))
    for path in ('cmd/other/main.go', 'internal/kmx/new/data.json',
                 'internal/other/utility.go', 'pkg/other/doc.go'):
        exercise(f'unowned package: {path}', additions={path: 'package other\n'}, want=ownership)
    exercise('top heading cannot cover packages', text=SELFTEST_MAP.replace(
        '| `cmd/tool` | Installed | Command adapter. |', '| `cmd/` | Installed | All commands. |'), want=ownership)
    exercise('empty ownership table', text=SELFTEST_MAP.replace(
        '| `pkg/view` | Library | Read-only projections. |', ''), want=ownership)
    exercise('empty responsibility', text=SELFTEST_MAP.replace(
        '| Library | Read-only projections. |', '| Library | |'), want=ownership)
    exercise('duplicate ownership row', text=SELFTEST_MAP.replace(
        '| `pkg/view` | Library | Read-only projections. |',
        '| `pkg/view` | Library | Read-only projections. |\n| `pkg/view` | Library | Duplicate. |'), want=ownership)
    exercise('root file cannot hide outside package', additions={'cmd/loose.go': 'package main'}, want=ownership)
    exercise('missing retirement anchor', text=SELFTEST_MAP.replace(
        'The plane module and image-build helper are absent.', ''), want=retired)
    for path in ('plane/source.go', 'internal/kmx/planebuild/build.go'):
        exercise('revived source: ' + path, additions={path: 'package retired'}, want=retired)
    module = 'github.com/kaimahi-agents/kaimahi/plane'
    for directive in (f'require {module} v0.1.0', f'require (\n {module} v0.1.0\n)',
                      f'replace {module} => ../plane', f'module {module}'):
        exercise('retired module: ' + directive, additions={'go.mod': directive}, want=retired)
    for declaration in (f'import "{module}/api"', f'import alias "{module}"',
                        f'import . "{module}/api"', f'import _ "{module}/api"',
                        f'import (\n alias "{module}/api"\n)',
                        f'import /* note */ ( _ `{module}/api` )',
                        f'import/* separator */alias "{module}/api"'):
        exercise('retired import: ' + declaration,
                 additions={'internal/kmx/app/app.go': 'package app\n' + declaration}, want=retired)
    exercise('raw text is not an import declaration', additions={
        'cmd/tool/main.go': f'package main\nvar history = `import "{module}/api"`\n'})
    exercise('historical comments are not imports', additions={
        'cmd/tool/main.go': f'package main\n/* import "{module}" */\n// import "{module}"\n',
        'go.mod': 'module example.invalid/fixture\n// require ' + module + ' v0.1.0\n'})
    exercise('no source inspected', missing=('cmd/tool/main.go', 'internal/kmx/app/app.go',
                                           'internal/support/util.go'), want=retired)
    exercise('missing module input', missing=('go.mod',), want=retired)
    if not CLAIMS or {fn.__name__ for fn in CLAIMS} != broken:
        failures.append(f'untested or empty claim registry: registered {[fn.__name__ for fn in CLAIMS]}, broken {sorted(broken)}')
    if exit_code(['fixture disagreement']) != 1 or exit_code([]) != 0:
        failures.append('the verdict ignores findings or rejects a clean map')
    if failures:
        for failure in failures:
            print('FAIL ' + failure, file=sys.stderr)
        print(f'\ncheck-repository-map self-test: {len(failures)} case(s) failed', file=sys.stderr)
        return 1
    print(f'check-repository-map self-test: {cases} cases; every claim independently broken; registry and verdict checked')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
