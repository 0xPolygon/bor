# Backfill StateSync Txs

The ```backfill-statesync-txs``` command receives a trusted file containing statesync txs and events from a time period and backfill it into the database. It walks over the block period checking any missing data and backfilling them. It writes just over KV database, which means the data which were supposed to already be on ancient will be now always at ancient.

## Options

- ```backfill-file```: Path of the file containing the backfill data

- ```datadir```: Path of the data directory to store information

- ```keystore```: Path of the data directory to store keys