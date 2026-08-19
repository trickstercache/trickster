# Parser and renderer gates frozen in docs/developer/mysql-release-contract.md.
# Go benchmark columns are: name, iterations, ns/op, B/op, allocs/op.
/^BenchmarkMySQLCompatibilityCorpus\/Analyze\// {
    measured++
    if ($3 > 250000 || $5 > 65536) {
        printf "FAIL analyzer gate: %s (%s ns/op, %s B/op)\n", $1, $3, $5
        failed = 1
    }
}

/^BenchmarkMySQLCompatibilityCorpus\/Render(Full|PartialHit)\// {
    measured++
    if ($3 > 25000 || $5 > 16384) {
        printf "FAIL renderer gate: %s (%s ns/op, %s B/op)\n", $1, $3, $5
        failed = 1
    }
}

END {
    if (measured == 0) {
        print "FAIL no MySQL acceptance benchmarks were found"
        exit 1
    }
    if (failed) {
        exit 1
    }
    printf "PASS MySQL analyzer/renderer acceptance gates (%d results)\n", measured
}
