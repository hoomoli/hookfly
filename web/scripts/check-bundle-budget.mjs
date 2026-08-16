import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

const dist = new URL("../dist/", import.meta.url);
const html = readFileSync(new URL("index.html", dist), "utf8");
const sources = [...html.matchAll(/<script[^>]+src="([^"]+\.js)"/g)].map((match) => match[1].replace(/^\//, ""));
if (sources.length === 0) throw new Error("no entry JavaScript found in dist/index.html");

const bytes = sources.reduce((total, source) => total + gzipSync(readFileSync(join(fileURLToPath(dist), source))).byteLength, 0);
const limit = 200 * 1024;
console.log(`entry JavaScript gzip: ${bytes} bytes / ${limit} bytes`);
if (bytes > limit) process.exitCode = 1;
