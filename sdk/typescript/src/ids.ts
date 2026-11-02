import { randomBytes } from 'node:crypto';

let lastMs = 0;
let seq = 0;

/** RFC 9562 UUIDv7 (time ordered, monotonic within this process). */
export function uuid7(): string {
  let ms = Date.now();
  if (ms <= lastMs) {
    ms = lastMs;
    seq += 1;
    if (seq > 0xfff) {
      ms += 1;
      seq = 0;
    }
  } else {
    seq = randomBytes(2).readUInt16BE(0) & 0x7ff;
  }
  lastMs = ms;
  const b = Buffer.alloc(16);
  b.writeUIntBE(ms, 0, 6);
  b[6] = 0x70 | ((seq >> 8) & 0x0f);
  b[7] = seq & 0xff;
  const r = randomBytes(8);
  r[0] = (r[0] & 0x3f) | 0x80;
  r.copy(b, 8);
  const h = b.toString('hex');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
