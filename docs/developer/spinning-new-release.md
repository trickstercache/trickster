# Spinning New Trickster Release

Users with push access to tricksterproxy/trickster (maintainers and owners) can spin releases.

To spin a new Trickster release, clone the repo, checkout the commit ID for the release, tag it with a release in semantic version format (`vX.Y.Z`), and push the tag back to the GitHub repository.

GitHub actions will detect the publishing of the new tag (so long as it's in the proper format) and cut a full release for the tag automatically.
