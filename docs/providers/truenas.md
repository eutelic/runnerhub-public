# TrueNAS provider

RunnerHub uses the same provider lifecycle on TrueNAS as on generic hosts. The current adapter calls the local `midclt` CLI with structured `app.create`, `app.get_instance`, `app.stop`, and `app.delete` requests. This deliberately avoids the removed legacy REST surface and keeps TrueNAS-specific behavior out of the scheduler.

The provider controls one configured instance root, creates deterministic app names, passes structured custom-app configuration, and never accepts arbitrary Compose YAML from the RunnerHub UI.
