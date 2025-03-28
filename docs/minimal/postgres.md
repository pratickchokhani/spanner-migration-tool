---
layout: default
title: PostgreSQL
parent: Minimal downtime migrations
nav_order: 2
---

# Minimal downtime migrations for PostgreSQL
{: .no_toc }

Spanner migration tool can be used to perform minimal downtime migration for PostgreSQL using the GUI or the CLI.

{: .highlight }
Following instructions assume you have setup SMT by following the instructions in the [installation](../install.md) guide.

<details open markdown="block">
  <summary>
    Table of contents
  </summary>
  {: .text-delta }
1. TOC
{:toc}
</details>

{: .important }
Before attempting a minimal downtime migration, ensure you have added the necessary permissions required in the GCP account. This is documented [here](../permissions.md).

## Source database configuration

### Allow Datastream to connect to PostgreSQL database

{: .important }
Spanner migration tool currently supports creating connection profiles via the IP whitelisting route only.

Follow the
[Datastream guidelines](https://cloud.google.com/datastream/docs/network-connectivity-options)
to allowlist datastream to access the source database.

- [IP allowlist](https://cloud.google.com/datastream/docs/network-connectivity-options#ipallowlists)
- [Forward SSH Tunneling](https://cloud.google.com/datastream/docs/network-connectivity-options#sshtunnel)
- [VPC Peering](https://cloud.google.com/datastream/docs/network-connectivity-options#privateconnectivity)

### Configure PostgreSQL database for CDC

Follow the guidelines for configuring [PostgreSQL](https://cloud.google.com/datastream/docs/configure-your-source-postgresql-database) here.

{: .warning }
It is often a good idea to create a connection profile and a Datastream stream directly via the GCP console to ensure that permissions and connectivity are correctly configured **before** proceeding with using
SMT for a minimal downtime migration. This can be done by following the [Datastream documentation](https://cloud.google.com/datastream/docs/create-a-stream).

## CLI

To run a minimal downtime schema and data migration:

        $ ./spanner-migration-tool schema-and-data --source=postgres \
            --source-profile='host=host,port=5432,user=user,password=pwd,dbN\
        ame=db,streamingCfg=streaming.json' \
            --target-profile='project=spanner-project,instance=spanner-insta\
        nce'

## UI

Follow the steps below to configure a minimal downtime migration are PostgreSQL:

1. [Connect to source database](../ui/connect-source.md).
2. [Connect to spanner instance](../ui/connect-spanner.md).
3. [Convert PostgreSQL schema to Spanner schema using the schema conversion workspace](../ui/schema-conv/schema-conv.md). Follow the documentation in the schema conversion workspace section of the documentation for different schema modifications that are supported by SMT. For guidance on specific data type conversion for Postgres, look at the [data conversion](../data-types/postgres.md) documentation.
4. [Configure datastream and dataflow details](../ui/prepare-migration/prepare.md).
5. Wait for the migration to be orchestrated by SMT.
6. Look at the list of [generated resources](../ui/prepare-migration/monitor.md/#generated-resources) for links to the Datastream stream and the Dataflow job crearted.
