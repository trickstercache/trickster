# Extending Trickster to Support a New Origin Type

Trickster 1.0 was written with extensibility in mind, and should be able to work with any time series database that has an HTTP-based API. In Trickster, we generically refer to our supported TSDB's as Origin Types. Some Origin Types are easier to implement and maintain than others, depending upon a host of factors that are covered later in this document. This document is meant to help anyone wishing to extend Trickster to support a new Origin Type, particularly in gauging the level of effort, understanding what is involved, and implementing the required interfaces and rules.

## Qualifications

Not every database server out there is a candidate for being fronted by Trickster. Trickster serves the specific purpose of accelerating the delivery of time series data sets, and will not benefit traditional relational databases, NoSQL, etc.

As mentioned, the database must be able to be queried for and return time series data via HTTP. Some databases that are not specifically TSDB's actually do support querying for and returning data in a time series format, and Trickster will support those cases as detailed below, so long as they have an HTTP API.

### Skills Needed

In addition to these requirements in the technology, there are also skills qualifications to consider.

Whether or not you've contributed to Open Source Software before, take a look at our Contributing guidelines so  you know how the process works for the Trickster project. If you are unfamiliar with the Forking Workflow, read up on it so that you are able to contribute to the project through Pull Requests.

Trickster is a 100% Go project, so you will need to have experience writing in Go, and in particular, data marshaling/unmarshaling and data set manipulation (sorting, merging, cropping, de-duplicating, etc.). You will need to have a good understanding of the prospective Origin Type's query language and response payload structure, so you can write the necessary parsing and modeling methods that allow Trickster to manipulate upstream HTTP requests and merge newly fetched data sets into the cache.

While this might sound daunting, it is actually much easier than it appears on the surface. Since Trickster's DeltaProxyCache engine does a lot of the heavy lifting, you only have to write a series of interface functions before finding yourself near the finish line. And since a few Origin Types are already implemented, you can use their implementations for references, since the logic for your prospective Origin Type should be similar.

## Interfaces

Trickster provides 2 required interfaces for enabling a new Origin Type: The Proxy Client and the Time Series

### Proxy Client Interface

The Proxy Client Interface ([code](https://github.com/Comcast/trickster/blob/next/internal/proxy/model/client.go)) is used by Trickster to manipulate HTTP requests and responses in order to accelerate the requests.

For your Proxy Client Implementation, you will need to know these things about the Origin:

- What URL paths and methods must be supported, and which engine through which to route each path (Basic HTTP Proxy, Object Proxy Cache, or Time Series Delta Proxy Cache). The proxy engines will call your client implementation's interface exports in order to service user requests.

- What data inputs the origin expects (Path, URL parameters, POST Data, HTTP Headers, cookies, etc.), and how to manipulate the query's time range when constructing those inputs to achieve a desired result.

The Proxy Client Interface Methods you will need to implement are broken into several groups of functionality, as follows.

#### Basic Getters

- `Configuration` returns the \*config.OriginConfig object for the origin.

- `Name` returns the configured name of the Origin Type instance.

- `HTTPClient` returns the reusable \*http.Client object that communicates with the Origin.

#### HTTP Request Routing and Handling

- `RegisterRoutes` registers all of the HTTP Paths that will be used by the Origin Type and map them to handlers written to service the various paths.

- `HealthHandler` this method is a standard HTTP Handler that can verify and report back the health of the upstream origin and the proxy's connection to it. You will certainly create at least one other handler in your Origin Type package, but this is the only one required for conformance to the Proxy Client interface.

#### Caching

- `DeriveCacheKey` inspects the client request and returns the corresponding cache key

#### Time Series Handling

- `UnmarshalTimeseries` deserializes an HTTP Response time series payload into a Go struct

- `MarshalTimeseries` seralizes a time-series struct into a serialized byte slice.

- `UnmarshalInstantaneous` deserializes an HTTP Response instantaneous payload into a Go struct. This may not be applicable to every potential Origin Type.

- `ParseTimeRangeQuery` inspects the client request and returns a corresponding timeseries.TimeRangeQuery

- `SetExtent` updates an upstream request's time range parameters as needed based on the delta gap analysis

- `FastForwardURL` returns the URL to the origin to collect Fast Forward data points based on the provided HTTP Request

### Time Series Interface

The Time Series Interface ([code](https://github.com/Comcast/trickster/blob/next/internal/timeseries/timeseries.go)) is used by Trickster to manipulate Time Series documents in order to maintain the cache and construct downstream client request bodies.

For your Time Series Implementation, you will need to know these things about the Origin:

- The structure of the response payload and how that translates into Go structs. More often than not, a prospective Origin Type offers an importable model to assist with this.

The Time Series Interface Methods you will need to implement are broken into several groups of functionality, as follows.

#### Getters

- `Extents` returns a list of the time ranges present in the cache
- `Step` returns the Step (the duration between each timestamp in the series)
- `SeriesCount` returns the number of series (e.g., graph lines) in the data set
- `ValueCount` returns the total number of values across all series in the data set

#### Setters

- `SetExtents` sets the list of the time ranges present in the cache
- `SetStep` sets the Step

#### Data Set Manipulation

- `Merge` merges a variadic list of time series into the base time series
- `Sort` chronologically sorts the values in each series in the time series
- `Copy` makes an new exact copy of the time series
- `Crop` removes any values from the time series that are outside of the provided time range

## Special Considerations

### Query Language Complexity

One of the main areas of consideration is the complexity of parsing and manipulating an inbound query. You will need to (1) determine if it is indeed a request for a timeseries; and if so (2) extract the requested time range and step duration for the query; and in the event of a partial cache hit, (3) adjust the time range for the query to a provided range - all of which allows the DeltaProxyCache to fetch just the needed sections of data from the upstream origin. Requirements 1 and 2 are functionality in `ParseTimeRangeQuery` while requirement 3 is the functionality of `SetExtent`. The overall complexity of this process can significantly affect the level of effort required to implement a new Origin Type.

In the example of Prometheus, the process was extremely simple: since, in the Prometheus HTTP API, time range queries have a separate http endpoint path from instantaneous queries, and because the time range is provided as separate query parameters from the query itself, the range is easily modified without Trickster having any knowledge of the underlying query or having to even parse it at all.

In the example of ClickHouse, the process is much harder: since the query language is a variant of SQL standard, the requested time ranges are embedded into the query itself behind the `WHERE` clause. In cases such as this, Trickster must have some way, either through (1) importing a database package that can deserialize the query, allow manipulation, and serialize the modified query for you; or (2) new parsing and search/replacement logic introduced in your own package - which allows the Client to interepret the time range and modify it with time ranges provided by the DeltaProxyCache. With ClickHouse, since it is a C++ project and Trickster is Golang, we could not import any package to handle this work, so we crafted a regular expression to match against inbound queries. This regex extracts the timerange (and any ClickHouse time modifiers like `startOfMinute`) and step value as well as any other areas that include the timerange, and then use simple builtin string functions to inject tokens in place of specific ranges in the provided query. Then, when SetExtent is called, those tokens are search/replaced with the provided time values.

### Data Model Considerations

Once you have the Client Interface implementation down and can interact with the upstream HTTP API, you will turn your attention to managing the response payload data and what to do with it, and that happens in your Timeseries Interface implementation. And like the Client interface, it will come with its own unique challenges.

The main consideration here is the format of the output and what challenges are presented by it. For example, does the payload include any required metadata (e.g., a count of total rows returned) that you will need to synthesize within your Timeseries after a `Merge`, etc. Going back to the ClickHouse example, since it is a columnar database that happens to have time aggregation functions, there are a million ways to formulate a query that yields time series results. That can have implications on the resulting dataset: which fields are the time and value fields, and what are the rest? Are all datapoints for all the series in a single large slice or have they been segregated into their own slices? Is the Timestamp in Epoch format, and if so, does it represent seconds or milliseconds? In order to support an upstream database, you may need to establish or adopt guidelines around these and other questions to ensure full compatibility. The ClickHouse plugin for Grafana requires that for each datapoint of the response, the first field is the timestamp and the second field is the numeric value - so we adopt and document the same guideline to conform to existing norms.

## Getting More Help

On the Gophers Slack instance, you can find us on the #trickster channel for any help you may need.
