#!/bin/sh
# The client runs as root and writes nothing to disk, so it needs neither the muc
# user nor /var/lib/muc. Both belong to muc-server, whose own preinstall creates
# them; on a host running both, this package deliberately leaves them alone.
#
# Nothing to do here. Kept as a no-op so the packaging keeps a stable script set.
exit 0
