import assert from "node:assert/strict";
import test from "node:test";
import { withArtifactPolicy } from "../src/artifact-policy";
const policy = { max_bytes: 65536, include: ["/results"] };
test("large-result policy is explicit and preserves projection", () => {
  assert.deepEqual(withArtifactPolicy(policy, "false", "bad", "bad"), policy);
  assert.deepEqual(withArtifactPolicy(policy, "true", "524288", "3600"), {
    ...policy,
    artifact: { max_bytes: 524288, ttl_seconds: 3600 },
  });
  assert.equal(
    withArtifactPolicy(policy, "true", "65536", "60").artifact?.ttl_seconds,
    60,
  );
  assert.equal(
    withArtifactPolicy(policy, "true", "1048576", "86400").artifact?.max_bytes,
    1048576,
  );
  assert.throws(() => withArtifactPolicy(policy, "maybe", "524288", "3600"));
  for (const size of ["", "bad", "1.5", "65535", "1048577"])
    assert.throws(() => withArtifactPolicy(policy, "true", size, "3600"));
  for (const ttl of ["", "bad", "60.5", "59", "86401"])
    assert.throws(() => withArtifactPolicy(policy, "true", "524288", ttl));
});
