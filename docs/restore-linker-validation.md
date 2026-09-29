# Restore Runtime Linker Validation

The 2026-09-29 change extends the existing opt-in StatefulMigration certification
path. It does not grant capability based on Node Ready or installed file names.

After GPU readiness, the controller prepares known GPU Operator driver-root
library directories in /etc/ld.so.conf.d/stateful-nvidia-driver.conf and runs
ldconfig. Preparation uses a bounded SSH command and an atomic file replacement.
It does not restart CRI-O or install a different driver.

Both preparation and the runtime verification probe run a clean-environment test:
libcuda.so.1 and libnvidia-ml.so.1 must load and cuda-checkpoint --help must exit
successfully. Existing package/hash/live CRI-O/config checks remain required.
Failure keeps the new-node certification path from granting restore capability.

Requirements:
- StatefulMigration runtime profile and migrationRuntime.certifyRestore=true.
- Reviewed restore-runtime package and matching manifest/hash/commit configuration.
- GPU driver readiness and host access to its mounted libraries.
- Noninteractive sudo and the existing SSH/bootstrap tool prerequisites.

Existing Ready nodes are not automatically reinstalled or recertified. Do not
manually set migration.dcnlab.com/restore-from-file=true to bypass this gate.
Use newly provisioned qualified test nodes, or a separately reviewed maintenance
procedure for existing nodes.

Local tests exercise missing libraries, helper failure, clean environment and
command safety. They do not prove GPU checkpoint/restore succeeds on a real VM.
See restore-runtime-rollout.md for package delivery and actual restore validation.
