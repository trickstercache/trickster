# Contributing

If you would like to contribute to this project you can do so through GitHub by forking the repository and sending a pull request. Please make note of the following:

* If you are a new contributor see: [Steps to Contribute](#steps-to-contribute)

* If you have a trivial fix or improvement, go ahead and create a pull request,
  addressing (with `@...`) a suitable maintainer of this repository (see
  [MAINTAINERS.md](MAINTAINERS.md)) in the description of the pull request.

* If you plan to do something more involved, first discuss your ideas
  on our slack channel, #trickster, on the Gophers slack instance.
  This will avoid unnecessary work and surely give you and us a good deal
  of inspiration.

* Relevant coding style guidelines are the [Go Code Review Comments](https://code.google.com/p/go-wiki/wiki/CodeReviewComments)
  and the _Formatting and style_ section of Peter Bourgon's [Go: Best
  Practices for Production
  Environments](http://peter.bourgon.org/go-in-production/#formatting-and-style).

* Before your contribution can be accepted, you must sign off your commits to signify acceptance of the [DCO](https://github.com/probot/dco#how-it-works).

## Reporting Feature Requests, Bugs, Vulnerabilities and other Issues

If you find a bug in Trickster, please file a detailed report as an Issue. We currently do not utilize an Issue template, but please be as thorough as possible in your report. There is no such thing as too much information.

Likewise, if you have a Feature Request, please file a detailed Issue, explaining the feature's functionality and use cases. Features should be useful to the broader community, so be sure to consider that before filing.

If you find a security vulnerability in Trickster, please follow the instructions in [SECURITY.MD](./SECURITY.MD).

## Steps to Contribute

Should you wish to work on an issue, please claim it first by commenting on the GitHub issue that you want to work on it. This is to prevent duplicated efforts from contributors on the same issue.

If you have questions about one of the issues, please comment on them and one of the maintainers will clarify it. For a quicker response, contact us on the #trickster slack channel.

For complete instructions on how to compile see: [Building From Source](https://github.com/trickstercache/trickster#building-from-source)

For quickly compiling and testing your changes do:

```bash
# For building
make
./bin/trickster

# For testing.
make test
```

## Pull Request Checklist

* Branch from the main branch and, if needed, rebase to the current main branch before submitting your pull request. If it doesn't merge cleanly with main you may be asked to rebase your changes.

* Commits should be as small as possible, while ensuring that each commit is correct independently (i.e., each commit should compile and pass tests).

* If your patch is not getting reviewed or you need a specific person to review it, you can @-reply a reviewer asking for a review in the pull request or a comment, or you can ask for a review on slack channel #trickster.

* All new code must include accompanying unit tests for as near to 100% coverage as possible. Our coverage rate for the project is approximately 98%, so all contributions should attain that level or higher. We may ask you to commit additional tests as required to ensure coverage is maintained before we merge the PR.
