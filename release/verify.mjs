import { readFileSync } from "node:fs"
import { join } from "node:path"
import { sha256 } from "./licenses.mjs"

export function inspectOCI(dir, platform = "linux/amd64") {
  function blob(descriptor) {
    if (!/^sha256:[a-f0-9]{64}$/.test(descriptor.digest)) throw Error("Invalid blob digest")
    const data = readFileSync(join(dir, "blobs/sha256", descriptor.digest.slice(7)))
    if (sha256(data) !== descriptor.digest.slice(7) || data.length !== descriptor.size) throw Error("Corrupted OCI blob")
    return data
  }
  const index = JSON.parse(readFileSync(join(dir, "index.json"), "utf8"))
  let descriptors = index.manifests
  if (descriptors.length === 1 && /image.index/.test(descriptors[0].mediaType)) descriptors = JSON.parse(blob(descriptors[0])).manifests
  const images = descriptors.filter((d) => `${d.platform?.os}/${d.platform?.architecture}` === platform)
  if (images.length !== 1) throw Error(`Expected exactly one ${platform} runtime`)
  const image = images[0], manifest = JSON.parse(blob(image))
  const config = JSON.parse(blob(manifest.config))
  for (const layer of manifest.layers) blob(layer)
  if (config.config.User !== "65532:65532") throw Error("Runtime must use UID/GID 65532")
  const predicates = new Map()
  for (const d of descriptors.filter((d) => d.annotations?.["vnd.docker.reference.type"] === "attestation-manifest")) {
    if (d.annotations["vnd.docker.reference.digest"] !== image.digest) throw Error("Attestation references wrong image")
    const attestation = JSON.parse(blob(d))
    blob(attestation.config)
    for (const layer of attestation.layers) {
      const statement = JSON.parse(blob(layer))
      if (!statement.subject?.some((s) => s.digest?.sha256 === image.digest.slice(7))) throw Error("Attestation subject mismatch")
      predicates.set(statement.predicateType, statement.predicate)
    }
  }
  const sbom = predicates.get("https://spdx.dev/Document")
  if (!sbom || sbom.spdxVersion !== "SPDX-2.3" || !sbom.packages?.length) throw Error("Missing SPDX SBOM")
  const provenance = predicates.get("https://slsa.dev/provenance/v0.2") ?? predicates.get("https://slsa.dev/provenance/v1")
  if (!provenance || !(provenance.buildType ?? provenance.buildDefinition?.buildType)) throw Error("Missing build provenance")
  return { digest: image.digest, config, sbom, provenance }
}
