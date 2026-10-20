# FabricNode AI Agent for FOXDEN Infrastructure

An interactive AI Agent written in Go for dataset discovery and automated workflow execution interacting with FOXDEN FabricNode services.

## Overview

The agent executes a 4-step proof-of-concept pipeline:
1. **Interactive Prompt**: Translates user questions into structured dataset search queries.
2. **FabricNode Data Auto-Discovery**: Queries FabricNode services to fetch matching datasets.
3. **Data Inspection**: Downloads dataset files and prints internal structure.
4. **Derived Dataset Registration**: Computes derived analytical outputs and registers the new dataset record into FabricNode.

---

## Instructions

### Build Project
```bash
make build
```

### Run Interactive CLI Prompt
```bash
make run
```

### Run Non-Interactive Single Query Mode
```bash
./bin/fabric-agent --query "Find all ID3A crystallography datasets with thaumatin"

# other query examples
# - Find all ID3A crystallography datasets with thaumatin
# - Get silicon standard observations on beamline ID1
# - Show me FAST beamline nanoparticle samples
```

### Create Tarball Archive
```bash
make package
```
