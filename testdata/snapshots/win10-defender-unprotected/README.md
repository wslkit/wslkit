# win10-defender-unprotected

Windows 10 22H2, WSL 2.7.13, two distributions, **read from an elevated
console** so the Defender exclusion list is present rather than UNKNOWN. That
is what makes this pair worth having: the elevated branch of DEF001 could not
be exercised by any earlier snapshot, because an unelevated read cannot see the
list at all.

Here nothing covers the WSL disks, so DEF001 leads with a WARN naming both.
`win10-defender-excluded` is the same machine after `doctor fix defender
--apply`, and DEF001 goes OK there, naming the rule that covered each disk.

The exclusion list in this file was derived from the other one by removing
exactly the two paths and three processes that the fix had added, which is what
the machine looked like ten minutes earlier and what was observed live before
the fix ran. Everything else, including the 23 real Host-Network-Service
events, is as captured.
