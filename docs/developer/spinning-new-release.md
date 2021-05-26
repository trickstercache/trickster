# Spinning New Trickster Release

Users with push access to trickstercache/trickster (Maintainers and owners) can spin releases.

To spin a new Trickster release, clone the repo, checkout the commit ID for the release, tag it with a release in semantic version format (`vX.Y.Z`), and push the tag back to the GitHub repository.

GitHub actions will detect the publishing of the new tag (so long as it's in the proper format) and cut a full release for the tag automatically.

The cut release will be published as a Draft, giving the Maintainer the opportunity to craft release notes in advance of the actual release.

Once a Trickster release is cut, the Maintainer must follow the directions in the [trickster-docker-images](https://github.com/trickstercache/trickster-docker-images) project to separately push the release to Docker Hub via automation.
