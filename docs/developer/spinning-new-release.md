# Spinning New Trickster Release

Users with push access to trickstercache/trickster (Maintainers and owners) can create new releases.

To spin a new Trickster release, clone the repo, checkout the commit ID for the release, tag it with a release in semantic version format (`vX.Y.Z`), and push the tag back to the GitHub repository.

A makefile target is provided to make this easier:
```bash
TAG_VERSION=v1.2.345 make create-tag
```
Prompts will ask for confirmation before creating the tag.

<details>
<summary>Example Output</summary>

```bash
% TAG_VERSION=v1.2.345 make create-tag
FYI: the last proper tag was: v0.0.9
FYI: the last beta tag was: v2.0.0-beta2
Create tag v1.2.345? [y/N] y
git tag v1.2.345
git push origin v1.2.345
Total 0 (delta 0), reused 0 (delta 0), pack-reused 0
To github.com:crandles/trickster.git
 * [new tag]           v1.2.345 -> v1.2.345
```

</details>

GitHub actions will detect the publishing of the new tag (so long as it's in the proper format) and cut a full release for the tag automatically.

The cut release will be published as a Draft, giving the Maintainer the opportunity to craft release notes in advance of the actual release.

Once a Trickster release is cut, the Maintainer must follow the directions in the [trickster-docker-images](https://github.com/trickstercache/trickster-docker-images) project to separately push the release to Docker Hub via automation.
