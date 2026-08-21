"use strict";

/* A small ZIP writer, so exporting never needs the server.
 *
 * Entries are deflated with the browser's own CompressionStream — the same
 * algorithm ZIP expects — and fall back to stored (uncompressed) where that
 * is unavailable. This exists instead of a vendored zip library because the
 * format's writer side is genuinely small, and because every dependency in
 * this path would be one more thing handling plaintext notes.
 */

const NG_CRC_TABLE = (function () {
	const table = new Uint32Array(256);
	for (let i = 0; i < 256; i++) {
		let c = i;
		for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
		table[i] = c >>> 0;
	}
	return table;
})();

function ngCRC32(bytes) {
	let c = 0xffffffff;
	for (let i = 0; i < bytes.length; i++) c = NG_CRC_TABLE[(c ^ bytes[i]) & 0xff] ^ (c >>> 8);
	return (c ^ 0xffffffff) >>> 0;
}

async function ngDeflate(bytes) {
	if (typeof CompressionStream === "undefined") return null;
	try {
		const stream = new Blob([bytes]).stream().pipeThrough(new CompressionStream("deflate-raw"));
		return new Uint8Array(await new Response(stream).arrayBuffer());
	} catch (err) {
		return null; // stored entries are still a valid archive
	}
}

// DOS timestamps: the format predates anything better, and readers expect one.
function ngDosTime(date) {
	const time = ((date.getHours() & 31) << 11) | ((date.getMinutes() & 63) << 5) | ((date.getSeconds() / 2) & 31);
	const day = (((date.getFullYear() - 1980) & 127) << 9) | (((date.getMonth() + 1) & 15) << 5) | (date.getDate() & 31);
	return { time: time, date: day };
}

function ngWriter(size) {
	const buf = new Uint8Array(size);
	let at = 0;
	return {
		u16(v) { buf[at++] = v & 0xff; buf[at++] = (v >>> 8) & 0xff; },
		u32(v) { buf[at++] = v & 0xff; buf[at++] = (v >>> 8) & 0xff; buf[at++] = (v >>> 16) & 0xff; buf[at++] = (v >>> 24) & 0xff; },
		bytes(b) { buf.set(b, at); at += b.length; },
		get offset() { return at; },
		done() { return buf.subarray(0, at); },
	};
}

// ngZip turns [{name, data}] into a Blob ready to download.
async function ngZip(entries) {
	const encoder = new TextEncoder();
	const stamp = ngDosTime(new Date());
	const prepared = [];
	let total = 0;

	for (const entry of entries) {
		const name = encoder.encode(entry.name);
		const raw = entry.data instanceof Uint8Array ? entry.data : encoder.encode(String(entry.data));
		const deflated = await ngDeflate(raw);
		const useDeflate = deflated && deflated.length < raw.length;
		const body = useDeflate ? deflated : raw;
		prepared.push({
			name: name,
			body: body,
			method: useDeflate ? 8 : 0,
			crc: ngCRC32(raw),
			rawSize: raw.length,
		});
		total += 30 + name.length + body.length + 46 + name.length;
	}

	const out = ngWriter(total + 22);
	const central = [];
	for (const item of prepared) {
		central.push({ item: item, offset: out.offset });
		out.u32(0x04034b50); // local file header
		out.u16(20);         // version needed
		out.u16(0x0800);     // UTF-8 names
		out.u16(item.method);
		out.u16(stamp.time);
		out.u16(stamp.date);
		out.u32(item.crc);
		out.u32(item.body.length);
		out.u32(item.rawSize);
		out.u16(item.name.length);
		out.u16(0);
		out.bytes(item.name);
		out.bytes(item.body);
	}

	const centralStart = out.offset;
	for (const { item, offset } of central) {
		out.u32(0x02014b50); // central directory header
		out.u16(20);
		out.u16(20);
		out.u16(0x0800);
		out.u16(item.method);
		out.u16(stamp.time);
		out.u16(stamp.date);
		out.u32(item.crc);
		out.u32(item.body.length);
		out.u32(item.rawSize);
		out.u16(item.name.length);
		out.u16(0);
		out.u16(0);
		out.u16(0);
		out.u16(0);
		out.u32(0);
		out.u32(offset);
		out.bytes(item.name);
	}

	out.u32(0x06054b50); // end of central directory
	out.u16(0);
	out.u16(0);
	out.u16(central.length);
	out.u16(central.length);
	out.u32(out.offset - centralStart);
	out.u32(centralStart);
	out.u16(0);
	return new Blob([out.done()], { type: "application/zip" });
}
