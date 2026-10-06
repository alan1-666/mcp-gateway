export interface ResultPage {
  operation_id: string;
  bytes: number;
  sha256: string;
  expires_at: string;
  format: string;
  offset: number;
  chunk: string;
  next_cursor?: string;
}

export function appendResultPage(
  operationID: string,
  previous: string,
  previousHash: string,
  page: ResultPage,
): string {
  const encoder = new TextEncoder();
  if (
    !page ||
    page.operation_id !== operationID ||
    page.format !== "json_utf8" ||
    !Number.isSafeInteger(page.bytes) ||
    page.bytes < 1 ||
    page.bytes > 1048576 ||
    !/^[a-f0-9]{64}$/.test(page.sha256) ||
    (previousHash && page.sha256 !== previousHash) ||
    !Number.isFinite(Date.parse(page.expires_at)) ||
    page.offset !== encoder.encode(previous).length ||
    typeof page.chunk !== "string" ||
    !page.chunk ||
    encoder.encode(page.chunk).length > 16384 ||
    (page.next_cursor !== undefined &&
      (typeof page.next_cursor !== "string" || page.next_cursor.length > 512))
  ) {
    throw new Error(
      "The gateway returned an invalid or changed result chunk. Restart reading the result.",
    );
  }
  const next = previous + page.chunk;
  const bytes = encoder.encode(next).length;
  if (
    bytes > page.bytes ||
    (page.next_cursor ? bytes >= page.bytes : bytes !== page.bytes)
  ) {
    throw new Error(
      "The result chunk has an inconsistent size or continuation cursor.",
    );
  }
  return next;
}
