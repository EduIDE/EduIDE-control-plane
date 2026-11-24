import type { components } from './api-client/schema.gen';

// The client package exports only the types of the user API. These are the admin and status schemas of the same spec.
type Schemas = components['schemas'];

export type PublicStatus = Schemas['PublicStatus'];
export type PublicTemplate = Schemas['PublicTemplate'];
export type ServiceLevel = Schemas['ServiceLevel'];
export type AdminStatus = Schemas['AdminStatus'];
export type AdminPools = Schemas['AdminPools'];
export type AdminPool = Schemas['AdminPool'];
export type AdminSession = Schemas['AdminSession'];
export type AdminSessionDetail = Schemas['AdminSessionDetail'];
export type AdminSessionList = Schemas['AdminSessionList'];
export type AdminWorkspace = Schemas['AdminWorkspace'];
export type AdminWorkspaceList = Schemas['AdminWorkspaceList'];
export type AdminProblems = Schemas['AdminProblems'];
export type AdminProblemEvent = Schemas['AdminProblemEvent'];
export type AdminTemplate = Schemas['AdminTemplate'];
export type AdminConfig = Schemas['AdminConfig'];

export type GitProvider = Schemas['GitProvider'];
export type GitPreflightResponse = Schemas['GitPreflightResponse'];
export type GitRepositories = Schemas['GitRepositories'];
export type GitApproval = Schemas['GitApproval'];
export type GitAccess = Schemas['GitAccess'];
