export const SCHEMES = ['weft:///'];

export function isRefUri(s: unknown): boolean {
  return typeof s === 'string' && SCHEMES.some((p) => s.startsWith(p));
}

export function splitProject(project: string): [string, string] {
  const p = project.trim().replace(/^\/+|\/+$/g, '');
  if (!p) throw new Error('project name must not be empty');
  const i = p.indexOf('/');
  if (i < 0) return ['default', p];
  const entity = p.slice(0, i);
  const proj = p.slice(i + 1);
  if (!entity || !proj || proj.includes('/')) throw new Error(`invalid project ${project}`);
  return [entity, proj];
}

/**
 * Ref kinds (docs/weft/DESIGN.md §10.2). Refs written with the earlier kind
 * names `op` and `call` are read as `function` and `span`.
 */
export type RefKind = 'object' | 'function' | 'table' | 'span';

const KIND_ALIASES: Record<string, RefKind> = { op: 'function', call: 'span' };

/** A weft:/// reference to an object, function, table or span (optionally with a path). */
export class Ref {
  constructor(
    public readonly entity: string,
    public readonly project: string,
    public readonly kind: RefKind,
    public readonly name: string,
    public readonly digest: string = '',
    public readonly extra: string[] = [],
  ) {}

  get projectId(): string {
    return `${this.entity}/${this.project}`;
  }

  uri(): string {
    const head = `weft:///${this.entity}/${this.project}/${this.kind}/`;
    const body = this.kind === 'object' || this.kind === 'function' ? `${this.name}:${this.digest}` : this.name;
    return head + body + this.extra.map((p) => '/' + encodeURIComponent(p)).join('');
  }

  withExtra(...parts: string[]): Ref {
    return new Ref(this.entity, this.project, this.kind, this.name, this.digest, [...this.extra, ...parts]);
  }

  toString(): string {
    return this.uri();
  }

  toJSON(): string {
    return this.uri();
  }

  static parse(uri: string): Ref {
    const scheme = SCHEMES.find((p) => uri.startsWith(p));
    if (!scheme) throw new Error(`not a weft ref: ${uri}`);
    const parts = uri.slice(scheme.length).split('/');
    if (parts.length < 4) throw new Error(`invalid ref: ${uri}`);
    const [entity, project, rawKind, namePart] = parts;
    const kind = KIND_ALIASES[rawKind] ?? rawKind;
    const extra = parts.slice(4).map(decodeURIComponent);
    if (kind === 'object' || kind === 'function') {
      const i = namePart.lastIndexOf(':');
      const name = i < 0 ? namePart : namePart.slice(0, i);
      const dg = i < 0 ? 'latest' : namePart.slice(i + 1);
      return new Ref(entity, project, kind, decodeURIComponent(name), dg, extra);
    }
    if (kind === 'table' || kind === 'span') return new Ref(entity, project, kind, namePart, '', extra);
    throw new Error(`unknown ref kind in ${uri}`);
  }
}
