# metrics-aggregator

Aggregate metrics to reduce cardinality by removing labels.

## options
```
--metrics-bind-address value                                         The address the metric endpoint binds to. (default: ":9090")
--metrics-path value                                                 The path under which to expose metrics. (default: "/metrics")
--target-url value                                                   The remote target metrics url to scrap metrics.
--aggregate-without-label value [ --aggregate-without-label value ]  The metrics will be aggregated over all label except listed labels. 
                                                                     Labels will be removed from the result vector, while all other labels are preserved in the output.
--include-metric value [ --include-metric value ]                    The name of the scrapped metrics which will be aggregated and exported. if its not set all metrics will be exported from target.
--add-prefix value                                                   The prefix which will be added to all exported metrics name.
--add-labelValue value [ --add-labelValue value ]                    The list of key=value pairs which will be added to all exported metrics.
--help, -h                                                           show help
```